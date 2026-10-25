import argparse, collections, concurrent.futures, hashlib, json, pathlib, signal, subprocess, sys, time, traceback
sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[3] / 'scripts'))
from system_test import Suite, DEVICES, ROOT, request, until, TERMINAL, interrupted

signal.signal(signal.SIGTERM, interrupted)
suite = Suite(argparse.Namespace(host='localhost', http_only=False, scenario='break-campaign', suite='routine', start_at=None))
started = time.time()
summary = {'started': started, 'project': suite.project, 'source': suite.metadata, 'phases': [], 'findings': []}
print('ARTIFACTS', suite.artifacts, flush=True)

EXTRA = '''
  - name: Reader Train
    steps:
      - {name: read_a, device: plate-reader-1}
      - {name: read_b, device: plate-reader-1, depends_on: [read_a]}
      - {name: read_c, device: plate-reader-1, depends_on: [read_b]}
  - name: Wide Join
    steps:
      - {name: fill_a, device: liquid-handler-1}
      - {name: fill_b, device: liquid-handler-1}
      - {name: heat_a, device: incubator-1}
      - {name: heat_b, device: incubator-1}
      - {name: scan_a, device: plate-reader-1}
      - {name: scan_b, device: plate-reader-1}
      - {name: join, device: liquid-handler-1, depends_on: [fill_a, fill_b, heat_a, heat_b, scan_a, scan_b]}
      - {name: final_read, device: plate-reader-1, depends_on: [join]}
  - name: Incubator Train
    steps:
      - {name: heat_a, device: incubator-1}
      - {name: heat_b, device: incubator-1, depends_on: [heat_a]}
      - {name: heat_c, device: incubator-1, depends_on: [heat_b]}
  - name: Liquid Train
    steps:
      - {name: dispense_a, device: liquid-handler-1}
      - {name: dispense_b, device: liquid-handler-1, depends_on: [dispense_a]}
  - name: Reader Then Heat
    steps:
      - {name: measure_first, device: plate-reader-1}
      - {name: heat_after, device: incubator-1, depends_on: [measure_first]}
'''

config = None

def setup_faults(seed='0', settings=None, duration='300ms', hang='700ms'):
    global config
    config = json.loads(suite.config.read_text())
    for device in DEVICES:
        values=config['services'][device]['environment']
        values.update(FAIL_PCT='0', HANG_PCT='0', HANG_DURATION=hang, STEP_DURATION=duration, CHAOS_SEED=seed)
        values.update((settings or {}).get(device, {}))
    suite.config.write_text(json.dumps(config, indent=2))
    suite.compose('up', '-d', '--no-build', '--no-deps', *DEVICES)
    for device,url in suite.instruments.items():
        expected=config['services'][device]['environment']
        until(lambda url=url,expected=expected: (lambda s: s['fail_pct']==int(expected['FAIL_PCT']) and s['hang_pct']==int(expected['HANG_PCT']))(request(url+'/status')),timeout=30,label='fault settings')
    (suite.directory/'instrument-config.json').write_text(json.dumps({d:config['services'][d]['environment'] for d in DEVICES},indent=2))

def create_many(names):
    with concurrent.futures.ThreadPoolExecutor(max_workers=12) as pool:
        ids=list(pool.map(suite.create,names))
    return ids

def settle_all(ids, timeout=100):
    deadline=time.monotonic()+timeout
    while time.monotonic()<deadline:
        views=[suite.view(r) for r in ids]
        if all(v['status'] in TERMINAL for v in views): return views
        time.sleep(.15)
    suite.snapshot('timeout')
    raise AssertionError('runs failed to settle: '+json.dumps(views))

def verify_causes(views):
    suite.assert_records()
    graphq='query($id:ID!){run(id:$id){steps{name status error attempts{jobId number executions{id outcome error retryable measurement{state sha256 dataRef}}}}}}'
    stats=collections.Counter()
    causes=[]
    for view in views:
        stats[view['status']]+=1
        g=suite.graph(graphq, {'id':view['id']},read_only=True)
        assert not g.get('errors'),g
        gs={s['name']:s for s in g['data']['run']['steps']}
        failures=[]
        for s in view['steps']:
            attempts=gs[s['name']]['attempts']
            for a in attempts:
                assert len(a['executions'])==1,('terminal run has unresolved attempt',a)
                x=a['executions'][0]
                if x['outcome'] in ['FAILED','HUNG']:
                    failures.append({'step':s['name'],'device':s['device_id'],'attempt':a['number'],'outcome':x['outcome'],'error':x['error']})
                if x.get('measurement') and x['outcome']=='COMPLETED':
                    m=x['measurement']
                    assert m['state'] in ['STORED','LOST'],m
                    if m['state']=='LOST': failures.append({'step':s['name'],'outcome':'DATA_LOST','error':s['error']})
                if a['number']>1:
                    prev=attempts[a['number']-2]['executions'][0]
                    assert prev['outcome'] in ['FAILED','HUNG'] and prev['retryable'],('unsafe retry',prev)
            if s['status']=='failed':
                assert failures or s['error']=='retry suppressed by run stop',('failed step without evidence',s)
        if view['status']=='failed': assert failures,('unexplained failed run',view)
        causes.append({'run':view['id'],'workflow':view['workflow_name'],'status':view['status'],'physical_faults':failures})
    return {'statuses':dict(stats),'runs':causes}

def phase(name, action, settings=None, seed='0', duration='300ms', hang='700ms'):
    if name not in ['06-random-mixed','07-zero-duration','08-zero-fault-drain']: return
    print('RUN',name,flush=True)
    suite.directory=suite.artifacts/name;suite.directory.mkdir();suite.runs=[]
    result={'name':name,'seed':seed,'settings':settings or {},'duration':duration,'hang':hang,'start':time.time()}
    summary['phases'].append(result)
    try:
        setup_faults(seed,settings,duration,hang)
        ids,expected=action()
        views=settle_all(ids)
        result.update(verify_causes(views))
        if expected: assert result['statuses']==expected,(result['statuses'],expected)
        result['result']='passed'
    except BaseException as error:
        result['result']='failed';result['error']=repr(error);result['traceback']=traceback.format_exc()
        suite.snapshot('failed')
        raise
    finally:
        result['seconds']=round(time.time()-result['start'],2)
        (suite.artifacts/'campaign.json').write_text(json.dumps(summary,indent=2))
        print(json.dumps(result),flush=True)

try:
    for service in ['executor',*DEVICES]: suite.base['services'][service]['image']='lab-test-20261025t062135z-12254-'+service+':latest'
    for d in DEVICES: suite.base['services'][d]['environment']['STEP_DURATION']='300ms'
    suite.start('bootstrap')
    workflows=suite.artifacts/'workflows.yaml';workflows.write_text((ROOT/'config/workflows.yaml').read_text()+EXTRA)
    container=suite.compose('ps','-q','executor').stdout.decode().strip()
    suite.command(['docker','cp',str(workflows),container+':/app/config/workflows.yaml'])
    assert len(request(suite.executor+'/workflows'))==7
    phase('01-concurrent-wide',lambda:(create_many(['PCR Amplification','Triple Assay','Wide Join','Reader Train','Incubator Train','Liquid Train']*2),{'completed':12}))
    phase('02-retry-exhaustion',lambda:(create_many(['Reader Train']*3+['Incubator Train']*3+['Liquid Train']*2),{'failed':8}),settings={DEVICES[0]:{'FAIL_PCT':'100'},DEVICES[1]:{'HANG_PCT':'100'},DEVICES[2]:{'FAIL_PCT':'100'}},seed='101')
    def overlap():
        ids=create_many(['Wide Join','Reader Train','Triple Assay','Incubator Train']*2)
        procs=[suite.chaos('disconnect','executor','5',background=True),suite.chaos('storage-down','6',background=True),suite.chaos('disconnect',DEVICES[1],'4',background=True)]
        for p in procs: assert p.wait(timeout=25)==0
        return ids, None
    phase('03-overlapping-outages',overlap,settings={DEVICES[1]:{'FAIL_PCT':'20','HANG_PCT':'30'},DEVICES[2]:{'FAIL_PCT':'20','HANG_PCT':'25'}},seed='2026')
    def cancels():
        ids=create_many(['PCR Amplification','Triple Assay','Wide Join','Reader Train','Incubator Train','Liquid Train']*2)
        with concurrent.futures.ThreadPoolExecutor(max_workers=12) as pool:
            list(pool.map(lambda r:request(suite.executor+'/runs/'+r+'/cancel',{}),ids[::2]))
        suite.chaos('kill','executor')
        return ids,None
    phase('04-cancel-restart',cancels,settings={DEVICES[1]:{'HANG_PCT':'40'},DEVICES[2]:{'FAIL_PCT':'30'}},seed='314')
    def driver_kills():
        ids=create_many(['Wide Join','Reader Train','Incubator Train','Liquid Train']*2)
        with concurrent.futures.ThreadPoolExecutor(max_workers=3) as pool:
            list(pool.map(lambda d:(suite.active(d,next(r for r in ids if any(s['device_id']==d for s in suite.view(r)['steps']))),suite.chaos('kill',d)),DEVICES))
        return ids,None
    phase('05-driver-kills',driver_kills,duration='1200ms')
    def random_phase():
        p=suite.chaos('random','--seed','47','--for','40',background=True)
        ids=[]
        while p.poll() is None:
            active=sum(suite.view(r)['status'] not in TERMINAL for r in ids)
            if active<5: ids.extend(create_many(['Wide Join','Reader Train','Triple Assay']))
            time.sleep(.4)
        assert p.returncode==0
        return ids,None
    phase('06-random-mixed',random_phase,settings={DEVICES[0]:{'FAIL_PCT':'10','HANG_PCT':'10'},DEVICES[1]:{'FAIL_PCT':'25','HANG_PCT':'25'},DEVICES[2]:{'FAIL_PCT':'20','HANG_PCT':'25'}},seed='47')
    phase('07-zero-duration',lambda:(create_many(['Reader Train','Incubator Train','Wide Join']*3),{'completed':9}),duration='0s',hang='1ms')
    phase('08-zero-fault-drain',lambda:(create_many(['PCR Amplification','Triple Assay','Wide Join']*2),{'completed':6}))
    suite.directory=suite.artifacts/'acceptance';suite.directory.mkdir();suite.runs=[]
    setup_faults(duration='300ms')
    suite.exercise('final-acceptance')
    summary['acceptance']='passed'
except BaseException as e:
    summary['failure']=repr(e);summary['traceback']=traceback.format_exc()
    print(summary['traceback'],flush=True)
finally:
    summary['cleanup_errors']=suite.cleanup()
    for service in ['executor',*DEVICES]:
        suite.command(['docker','image','rm',f'{suite.project}-{service}:latest'],check=False)
    summary['finished']=time.time();summary['seconds']=round(summary['finished']-started,2)
    (suite.artifacts/'campaign.json').write_text(json.dumps(summary,indent=2))
    suite.log.close()
    print('FINISHED',suite.artifacts,summary.get('failure'),flush=True)
