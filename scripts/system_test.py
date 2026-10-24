"""Isolated, fail-fast end-to-end checks against independent instrument ledgers."""
import argparse
import datetime
import hashlib
import json
import os
import pathlib
import random
import socket
import signal
import subprocess
import sys
import threading
import time
import traceback
import urllib.error
import urllib.request
from system_config import isolated

ROOT = pathlib.Path(__file__).resolve().parents[1]
DEVICES = ['liquid-handler-1', 'incubator-1', 'plate-reader-1']
FAULTS = ['LH_FAIL_PCT', 'LH_HANG_PCT', 'INC_FAIL_PCT', 'INC_HANG_PCT', 'PR_FAIL_PCT', 'PR_HANG_PCT']
TERMINAL = {'completed', 'failed', 'cancelled'}


def request(url, body=None, json_mode=True):
    data = None if body is None else json.dumps(body).encode()
    deadline = time.monotonic() + 15
    while True:
        req = urllib.request.Request(url, data=data, headers={'Content-Type': 'application/json'})
        try:
            with urllib.request.urlopen(req, timeout=4) as response:
                return json.load(response) if json_mode else response.read()
        except OSError as error:
            if body is not None or time.monotonic() >= deadline:
                raise urllib.error.URLError(f'{url}: {error}') from error
            time.sleep(0.2)


def until(check, timeout=120, label='condition'):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            value = check()
            if value:
                return value
        except (OSError, urllib.error.URLError, ValueError) as error:
            last = error
        time.sleep(0.1)
    raise AssertionError(f'timed out waiting for {label}: {last}')


def assert_retry_exhaustion(executions):
    assert len(executions) == 3, "retry budget not exhausted"


def fixture(device, step, kind):
    for seed in range(10000):
        def roll(attempt):
            text = f'{seed}:{device}:{step}:{attempt}'
            return int.from_bytes(hashlib.sha256(text.encode()).digest()[:4], 'big') % 100
        if roll(0) < 50 and roll(1) >= 50:
            return str(seed)
    raise AssertionError('no deterministic fault fixture')


class Suite:
    def __init__(self, args):
        self.args = args
        stamp = datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ')
        self.project = f'lab-test-{stamp.lower()}-{os.getpid()}'
        self.artifacts = ROOT / 'artifacts' / self.project
        self.artifacts.mkdir(parents=True)
        self.config = self.artifacts / 'compose.json'
        self.env = dict(os.environ)
        self.env.update({key: '0' for key in FAULTS})
        self.env.update(CHAOS_SEED='0', HANG_DURATION='30s', COMPOSE_PROJECT_NAME=self.project)
        self.base = json.loads(subprocess.check_output(['docker', 'compose', 'config', '--format', 'json'], cwd=ROOT, env=self.env))
        self.base = isolated(self.base, self.project, f'172.28.{random.randrange(2, 250)}')
        # Docker may reallocate an unspecified host port on container restart.
        ports = iter(random.sample(range(20000, 60000), 32))
        for service in self.base['services'].values():
            for port in service.get('ports', []):
                port['published'] = str(next(ports))
        for service in self.base['services'].values():
            service.setdefault('labels', {})['lab.test-owner'] = self.project
        self.config.write_text(json.dumps(self.base, indent=2))
        self.env['COMPOSE_FILE'] = str(self.config)
        self.results = []
        self.processes = []
        self.directory = self.artifacts
        self.runs = []
        self.host = args.host
        self.log = open(self.artifacts / 'commands.log', 'a')
        self.metadata = {'project': self.project, 'commit': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT).decode().strip(), 'dirty': subprocess.check_output(['git', 'status', '--short'], cwd=ROOT).decode(), 'args': vars(args)}
        (self.artifacts / 'manifest.json').write_text(json.dumps(self.metadata, indent=2))

    def command(self, args, *, data=None, timeout=180, check=True):
        self.log.write(json.dumps({'time': time.time(), 'command': args}) + '\n')
        self.log.flush()
        result = subprocess.run(args, cwd=ROOT, env=self.env, input=data, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=timeout)
        self.log.write(result.stdout.decode(errors='replace'))
        self.log.flush()
        if check and result.returncode:
            raise AssertionError(f'command failed ({result.returncode}): {args}; see {self.artifacts}/commands.log')
        return result

    def compose(self, *args, **kwargs):
        return self.command(['docker', 'compose', *args], **kwargs)

    def start(self, name):
        self.directory = self.artifacts / name
        self.directory.mkdir()
        self.runs = []
        config = json.loads(json.dumps(self.base))
        for device, prefix in zip(DEVICES, ['LH', 'INC', 'PR']):
            values = config['services'][device]['environment']
            for key in ['FAIL_PCT', 'HANG_PCT']:
                values[key] = '0'
            values['CHAOS_SEED'] = '0'
            values['HANG_DURATION'] = '30s'
        parts = name.split(':')
        if parts[0] in ('failure', 'hang'):
            device = parts[1]
            step = {'liquid-handler-1': 'fill_sample_plate', 'incubator-1': 'incubate_samples', 'plate-reader-1': 'read_plate'}[device]
            kind = 'FAIL_PCT' if parts[0] == 'failure' else 'HANG_PCT'
            values = config['services'][device]['environment']
            safe_fixture = len(parts) < 3 and device != DEVICES[0]
            values[kind] = '50' if safe_fixture else '100'
            if safe_fixture:
                values['CHAOS_SEED'] = fixture(device, step, kind)
            if len(parts) > 2 and parts[2] == 'long':
                values['HANG_DURATION'] = '60s'
                config['services']['executor']['environment']['RECOVERY_TIMEOUT'] = '75s'
        self.config.write_text(json.dumps(config, indent=2))
        (self.directory / 'config.json').write_text(json.dumps(config, indent=2))
        self.compose('up', '-d', 'postgres', 'nats', 'storage')
        until(lambda: self.compose('exec', '-T', 'postgres', 'pg_isready', '-h', '127.0.0.1', '-U', 'lab_admin', '-d', 'lab_default', check=False).returncode == 0, label='Postgres readiness')
        self.compose('exec', '-T', 'postgres', 'psql', '-v', 'ON_ERROR_STOP=1', '-U', 'lab_admin', '-d', 'lab_default', data=(ROOT / 'db/init.sql').read_bytes())
        if self.host == 'localhost':
            port = self.compose('port', 'postgres', '5432').stdout.decode().strip().rsplit(':', 1)[1]
            try:
                with socket.create_connection((self.host, int(port)), timeout=1):
                    pass
            except OSError:
                self.host = 'host.docker.internal'
        if name == 'integration':
            def address(service, port):
                value = self.compose('port', service, str(port)).stdout.decode().strip()
                return value.rsplit(':', 1)[1]
            host = self.host
            if host == 'localhost':
                try:
                    with socket.create_connection((host, int(address('postgres', 5432))), timeout=1):
                        pass
                except OSError:
                    host = 'host.docker.internal'
            self.integration_env = dict(self.env, LAB_TEST_DATABASE=f'postgres://lab_admin:lab_password@{host}:{address("postgres", 5432)}/executor_db?sslmode=disable', LAB_TEST_NATS=f'nats://{host}:{address("nats", 4222)}', LAB_TEST_S3=f'http://{host}:{address("storage", 8333)}')
            return
        self.compose('up', '-d', '--no-build', 'executor', *DEVICES)
        def endpoint(service, port):
            address = self.compose('port', service, str(port)).stdout.decode().strip()
            return f'http://{self.host}:{address.rsplit(":", 1)[1]}'
        self.executor = endpoint('executor', 5001)
        self.storage = endpoint('storage', 8333)
        self.instruments = {device: endpoint(device, 7000) for device in DEVICES}
        until(lambda: request(self.executor + '/health'), label='executor readiness')
        for url in self.instruments.values():
            until(lambda url=url: request(url + '/status'), label='instrument readiness')
        if not self.args.http_only:
            assert not self.graph('{ __schema { queryType { name } mutationType { name } } }').get('errors'), 'GraphQL not ready'
        self.snapshot('ready')

    def graph(self, query, variables=None):
        return request(self.executor + '/graphql', {'query': query, 'variables': variables or {}})

    def create(self, workflow=None):
        run = request(self.executor + '/runs', {} if workflow is None else {'workflow_name': workflow})
        self.runs.append(run['id'])
        request(self.executor + '/runs/' + run['id'] + '/start', {})
        return run['id']

    def view(self, run):
        return request(self.executor + '/runs/' + run)

    def ledger(self):
        return {d: request(url + '/status') for d, url in self.instruments.items()}

    def active(self, device, run):
        return until(lambda: next((x for x in request(self.instruments[device] + '/status')['executions'] if x['run_id'] == run and x['outcome'] == 'running'), None), label=f'active {device} for {run}')

    def chaos(self, *args, background=False):
        command = ['bash', str(ROOT / 'scripts/chaos.sh'), *args]
        if background:
            log = open(self.directory / f'fault-{len(self.processes)}.log', 'wb')
            process = subprocess.Popen(command, cwd=ROOT, env=self.env, stdout=log, stderr=subprocess.STDOUT)
            self.processes.append((process, log))
            return process
        return self.command(command)

    def finish(self, run, expected='completed', timeout=300):
        view = until(lambda: (lambda v: v if v['status'] in TERMINAL else None)(self.view(run)), timeout, 'terminal run ' + run)
        assert view['status'] == expected, f'{run}: expected {expected}, got {view}'
        return view

    def assert_records(self):
        ledger = self.ledger()
        executions = [(d, x) for d, v in ledger.items() for x in v['executions'] if x['run_id'] in self.runs]
        jobs = [x['job_id'] for _, x in executions]
        assert len(jobs) == len(set(jobs)), 'duplicate physical execution for a job'
        for device in DEVICES:
            own = sorted([x for d, x in executions if d == device], key=lambda x: x['started_at'])
            for left, right in zip(own, own[1:]):
                assert left['finished_at'] is not None and left['finished_at'] <= right['started_at'], 'device overlap'
        for run in self.runs:
            view = self.view(run)
            assert view['status'] in TERMINAL, 'unsettled run'
            by_name = {s['name']: s for s in view['steps']}
            for step in view['steps']:
                physical = [x for _, x in executions if x['run_id'] == run and x['step_name'] == step['name']]
                assert len(physical) <= (1 if step['device_id'] == DEVICES[0] else 3), 'retry budget exceeded'
                if step['status'] == 'completed':
                    assert physical and physical[-1]['outcome'] == 'completed', 'unbacked completion'
                    if step['device_id'] == DEVICES[2]:
                        ref = step['data_ref']
                        assert ref and ref.startswith('s3://'), 'missing data reference'
                        digest = hashlib.sha256(request(self.storage + '/' + ref[5:], json_mode=False)).hexdigest()
                        assert digest == physical[-1]['data_sha256'], 'measurement hash mismatch'
                for x in physical:
                    for dependency in step['depends_on']:
                        prior = [p for _, p in executions if p['run_id'] == run and p['step_name'] == dependency]
                        assert prior and prior[-1]['outcome'] == 'completed' and prior[-1]['finished_at'] <= x['started_at'], 'dependency ordering violation'
            if not self.args.http_only:
                query = '''query($id:ID!){run(id:$id){id status cancellationRequested dispatchPaused steps{name status attempts{jobId number recoveryError executions{id outcome measurement{state sha256 dataRef}}}}}}'''
                body = self.graph(query, {'id': run})
                assert not body.get('errors'), body
                graph = body['data']['run']
                assert graph['status'].lower() == view['status'], 'HTTP/GraphQL mismatch'
                assert not graph['dispatchPaused'], 'terminal run retained an unresolved dispatch pause'
                observed = {x['id'] for s in graph['steps'] for a in s['attempts'] for x in a['executions']}
                actual = {x['execution_id'] for _, x in executions if x['run_id'] == run}
                assert observed == actual, 'GraphQL lost physical execution history'
        self.snapshot('settled')

    def snapshot(self, name):
        result = {'time': time.time(), 'runs': {}, 'instruments': {}}
        for run in self.runs:
            try:
                result['runs'][run] = self.view(run)
            except OSError as error:
                result['runs'][run] = {'unavailable': str(error)}
        for device, url in self.instruments.items():
            try:
                result['instruments'][device] = request(url + '/status')
            except OSError as error:
                result['instruments'][device] = {'unavailable': str(error)}
        (self.directory / (name + '.json')).write_text(json.dumps(result, indent=2))

    def exercise(self, name):
        if name == 'integration':
            for package in ['./services/common/lab', './services/driver', './services/executor']:
                result = subprocess.run(['go', 'test', '-cover', package, '-run', 'Integration', '-count=1'], cwd=ROOT, env=self.integration_env, capture_output=True, timeout=180)
                (self.directory / (package.rsplit('/', 1)[-1] + '.log')).write_bytes(result.stdout + result.stderr)
                assert result.returncode == 0, result.stdout.decode() + result.stderr.decode()
            return
        if name in ('happy', 'triple', 'final-acceptance'):
            env = dict(self.env, EXEC=self.executor, S3=self.storage, INSTRUMENTS=' '.join(self.instruments.values()))
            args = ['bash', 'scripts/acceptance.sh'] + (['Triple Assay'] if name == 'triple' else [])
            output = subprocess.run(args, cwd=ROOT, env=env, capture_output=True, timeout=120)
            (self.directory / 'acceptance.log').write_bytes(output.stdout + output.stderr)
            assert output.returncode == 0, output.stdout.decode() + output.stderr.decode()
            for line in output.stdout.decode().splitlines():
                if line.startswith('run: '):
                    self.runs.append(line[5:])
        elif name == 'graphql':
            body = self.graph('mutation { createRun { id status } }')
            assert not body.get('errors'), body
            run = body['data']['createRun']['id']
            self.runs.append(run)
            body = self.graph('mutation($id:ID!){startRun(id:$id){id status}}', {'id': run})
            assert not body.get('errors'), body
            self.finish(run)
        elif name == 'concurrent':
            runs = [self.create(), self.create('Triple Assay')]
            for run in runs:
                self.finish(run)
        elif name.startswith('cancel'):
            if name == 'cancel-pending':
                view = request(self.executor + '/runs', {})
                run = view['id'];self.runs.append(run)
            else:
                run = self.create();self.active(DEVICES[0], run)
            request(self.executor + '/runs/' + run + '/cancel', {})
            self.finish(run, 'cancelled')
        elif name.startswith(('failure:', 'hang:')):
            run = self.create()
            parts = name.split(':')
            fail = parts[1] == DEVICES[0] or len(parts) > 2
            view = self.finish(run, 'failed' if fail else 'completed')
            if fail and parts[1] != DEVICES[0]:
                physical = [x for x in self.ledger()[parts[1]]['executions'] if x['run_id'] == run]
                assert len(physical) == 3, 'persistent retryable failure did not exhaust exactly three attempts'
        elif name.startswith('kill:'):
            service = name.split(':')[1];run = self.create()
            device = service if service in DEVICES else DEVICES[0]
            before = self.active(device, run)
            self.chaos('kill', service)
            self.finish(run, 'failed' if service == DEVICES[0] else 'completed')
            if service in DEVICES:
                rows = [x for x in self.ledger()[service]['executions'] if x['execution_id'] == before['execution_id']]
                assert rows and rows[0]['outcome'] == 'hung', 'kill missed active execution'
        elif name.startswith('disconnect:'):
            service = name.split(':')[1];run = self.create();self.active(service if service in DEVICES else DEVICES[0], run)
            self.chaos('disconnect', service, '6')
            self.finish(run)
        elif name == 'unresolved':
            run = self.create();self.active(DEVICES[0], run)
            process = self.chaos('disconnect', DEVICES[0], '55', background=True)
            time.sleep(47)
            view = self.view(run)
            assert view['status'] == 'running' and any(s['error'] for s in view['steps']), 'deadline hid uncertainty'
            self.snapshot('unresolved')
            assert process.wait(timeout=20) == 0
            self.finish(run)
        elif name in ('storage', 'storage-driver'):
            run = self.create();self.active(DEVICES[2], run)
            process = self.chaos('storage-down', '8', background=True)
            if name == 'storage-driver':
                time.sleep(4);self.chaos('kill', DEVICES[2])
            assert process.wait(timeout=30) == 0
            self.finish(run)
        elif name.startswith('restart:'):
            service = name.split(':')[1];run = self.create();self.active(DEVICES[0], run)
            container = self.compose('ps', '-q', service).stdout.decode().strip()
            self.command(['docker', 'kill', container]);self.command(['docker', 'start', container])
            self.finish(run)
        elif name.startswith('random:'):
            seed = name.split(':')[1];process = self.chaos('random', '--seed', seed, '--for', '60', background=True)
            while process.poll() is None:
                active = sum(self.view(r)['status'] not in TERMINAL for r in self.runs)
                if active < 2:
                    self.create()
                time.sleep(0.5)
            assert process.returncode == 0, 'random injection failed'
            for run in self.runs:
                self.finish(run)
        else:
            raise ValueError('unknown scenario: ' + name)
        self.assert_records()

    def cleanup(self):
        errors = []
        for process, log in self.processes:
            try:
                if process.poll() is None:
                    process.terminate();process.wait(timeout=15)
            except Exception as error:
                errors.append(str(error));process.kill()
            log.close()
        self.processes = []
        try:
            if hasattr(self, 'instruments') and self.directory.name != 'integration':
                self.snapshot('diagnostics')
            logs = self.compose('logs', '--no-color', '--timestamps', check=False, timeout=30)
            (self.directory / 'services.log').write_bytes(logs.stdout)
        except Exception as error:
            errors.append('diagnostics: ' + str(error))
        try:
            self.compose('down', '-v', '--remove-orphans', timeout=90)
        except Exception as error:
            errors.append('cleanup: ' + str(error))
        return errors

    def run(self):
        routine = ['integration', 'happy', 'triple', 'kill:executor'] + ['disconnect:' + s for s in ['executor'] + DEVICES] + ['kill:' + d for d in DEVICES] + ['storage', 'failure:incubator-1', 'failure:plate-reader-1', 'failure:incubator-1:exhaust', 'failure:liquid-handler-1', 'hang:incubator-1', 'hang:plate-reader-1', 'hang:liquid-handler-1', 'concurrent', 'cancel-pending', 'cancel-active', 'random:7']
        if not self.args.http_only:
            routine.append('graphql')
        extended = ['restart:nats', 'restart:postgres', 'unresolved', 'storage-driver', 'hang:incubator-1:long', 'random:11', 'random:23', 'random:47']
        scenarios = [self.args.scenario] if self.args.scenario else routine + (extended if self.args.suite == 'extended' else []) + ['final-acceptance']
        failure = None
        try:
            if self.args.scenario != 'integration':
                self.compose('build', 'executor', *DEVICES, timeout=900)
            for name in scenarios:
                print('RUN', name, flush=True);started = time.monotonic()
                result = {'scenario': name, 'status': 'failed'}
                try:
                    self.start(name);self.exercise(name);result['status'] = 'passed'
                except BaseException as error:
                    result['error'] = repr(error);result['traceback'] = traceback.format_exc();failure = error
                finally:
                    cleanup = self.cleanup()
                    result['cleanup_errors'] = cleanup
                    result['seconds'] = round(time.monotonic() - started, 2)
                    if cleanup:
                        result['status'] = 'failed';failure = failure or RuntimeError(str(cleanup))
                    self.results.append(result)
                    print(result['status'].upper(), name, result['seconds'], result.get('error', ''), flush=True)
                if failure:
                    break
        except BaseException as error:
            failure = error
            self.cleanup()
        finally:
            completed = {r['scenario'] for r in self.results}
            summary = {'results': self.results, 'not_run': [s for s in scenarios if s not in completed], 'failure': repr(failure) if failure else None}
            (self.artifacts / 'results.json').write_text(json.dumps(summary, indent=2))
            print('Artifacts:', self.artifacts, flush=True)
            self.log.close()
        return 1 if failure else 0


def interrupted(signum, frame):
    raise KeyboardInterrupt(f'interrupted by signal {signum}')


if __name__ == '__main__':
    signal.signal(signal.SIGTERM, interrupted)
    parser = argparse.ArgumentParser()
    parser.add_argument('--suite', choices=['routine', 'extended'], default='routine')
    parser.add_argument('--scenario')
    parser.add_argument('--http-only', action='store_true', help='explicit pre-GraphQL implementation stage')
    parser.add_argument('--host', default=os.environ.get('LAB_TEST_HOST', 'localhost'))
    sys.exit(Suite(parser.parse_args()).run())
