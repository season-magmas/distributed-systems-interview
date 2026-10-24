"""Create a disposable stack, exercise its web UI, verify records, then clean up."""
import argparse
import json
import os
import signal
import subprocess
import sys

from system_test import DEVICES, ROOT, Suite, interrupted


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--host', default=os.environ.get('LAB_TEST_HOST', 'localhost'))
    args = parser.parse_args()
    args.http_only = False
    args.scenario = 'web-ui'
    args.suite = 'routine'
    args.start_at = None
    suite = Suite(args)
    result = {'status': 'failed', 'checks': []}
    try:
        print('Building disposable web UI stack', flush=True)
        suite.compose('build', 'executor', *DEVICES, timeout=900)
        suite.start('web-ui')
        env = dict(os.environ, LAB_UI_URL=suite.executor, LAB_UI_ARTIFACTS=str(suite.directory))
        subprocess.run(['node', str(ROOT / 'scripts/web-ui-smoke.mjs')], env=env,
                       check=True, timeout=180)
        result['checks'].append('browser')
        browser = json.loads((suite.directory / 'browser-result.json').read_text())
        suite.runs.extend(browser['runs'])
        suite.assert_records()
        result['checks'].append('independent ledger and measurement hashes')
        suite.exercise('final-acceptance')
        result['checks'].append('HTTP acceptance')
        result['status'] = 'passed'
    except BaseException as error:
        result['error'] = repr(error)
        print(result['error'], file=sys.stderr, flush=True)
    finally:
        result['cleanup_errors'] = suite.cleanup()
        for service in ['executor', *DEVICES]:
            tag = f'{suite.project}-{service}:latest'
            if suite.command(['docker', 'image', 'inspect', tag], check=False).returncode == 0:
                deletion = suite.command(['docker', 'image', 'rm', tag], check=False)
                if deletion.returncode:
                    result['cleanup_errors'].append('could not remove test image ' + tag)
        if result['cleanup_errors']:
            result['status'] = 'failed'
        (suite.artifacts / 'web-ui-results.json').write_text(json.dumps(result, indent=2))
        suite.log.close()
        print(json.dumps(result), flush=True)
        print('Artifacts:', suite.artifacts, flush=True)
    return 0 if result['status'] == 'passed' else 1


if __name__ == '__main__':
    signal.signal(signal.SIGTERM, interrupted)
    sys.exit(main())
