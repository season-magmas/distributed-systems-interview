import argparse,base64,json,pathlib,signal,subprocess,sys,time,traceback,urllib.request
sys.path.insert(0,str(pathlib.Path(__file__).resolve().parents[3]/'scripts'))
from system_test import Suite,DEVICES,ROOT,request,until,interrupted
signal.signal(signal.SIGTERM,interrupted)
s=Suite(argparse.Namespace(host='localhost',http_only=False,scenario='boundaries',suite='routine',start_at=None))
summary={'project':s.project,'source':s.metadata,'checks':[],'started':time.time()}
print('ARTIFACTS',s.artifacts,flush=True)
try:
    s.compose('build','executor',*DEVICES,timeout=180)
    for d in DEVICES:s.base['services'][d]['environment']['STEP_DURATION']='1500ms'
    s.start('boundaries')
    cfg=json.loads(s.config.read_text())
    for d in DEVICES:cfg['services'][d]['environment']['HANG_DURATION']='500ms'
    s.config.write_text(json.dumps(cfg,indent=2))
    s.compose('up','-d','--no-build','--no-deps',*DEVICES)
    extra='''
  - name: Boundary Reader
    steps:
      - {name: boundary_read, device: plate-reader-1}
      - {name: after_read, device: incubator-1, depends_on: [boundary_read]}
'''
    wf=s.artifacts/'workflows.yaml';wf.write_text((ROOT/'config/workflows.yaml').read_text()+extra)
    executor=s.compose('ps','-q','executor').stdout.decode().strip()
    s.command(['docker','cp',str(wf),executor+':/app/config/workflows.yaml'])
    print('RUN object-conflict-repair',flush=True)
    run=s.create('Boundary Reader')
    x=s.active(DEVICES[2],run)
    key=f"runs/{run}/jobs/{x['job_id']}/executions/{x['execution_id']}/measurement.csv"
    def put(data):
        with urllib.request.urlopen(urllib.request.Request(s.storage+'/lab-data/'+key,data=data,method='PUT'),timeout=5) as reply:assert reply.status in [200,204]
    put(b'deliberately corrupted object')
    paused=until(lambda:(lambda v:v if v['steps'][0].get('error') and 'hash conflict' in v['steps'][0]['error'] else None)(s.view(run)),timeout=15,label='hash conflict pause')
    assert paused['status']=='running' and paused['steps'][1]['status']=='pending'
    s.snapshot('hash-conflict')
    raw=s.compose('exec','-T',DEVICES[2],'cat',f"/spool/{x['job_id']}.json").stdout
    job=json.loads(raw);original=base64.b64decode(job['Bytes']);assert original
    s.chaos('kill',DEVICES[2])
    put(original)
    view=s.finish(run,timeout=45)
    s.assert_records()
    summary['checks'].append({'name':'object-conflict-repair','result':'passed','run':run,'observed':'hash conflict paused dependent; reader restart and exact-byte repair completed without rereading'})
    print('PASSED object-conflict-repair',flush=True)
    print('RUN spool-capacity-recovery',flush=True)
    # Fill the test-owned spool to its configured logical limit before authorization.
    s.compose('exec','-T',DEVICES[2],'truncate','-s','67108864','/spool/test-ballast')
    run=s.create('Boundary Reader')
    driver=s.compose('ps','-q',DEVICES[2]).stdout.decode().strip()
    until(lambda:int(s.command(['docker','inspect','-f','{{.RestartCount}}',driver]).stdout)>0,timeout=15,label='capacity refusal restart')
    physical=[v for v in request(s.instruments[DEVICES[2]]+'/status')['executions'] if v['run_id']==run]
    assert not physical,'spool-full command physically executed'
    s.snapshot('spool-full')
    s.compose('exec','-T',DEVICES[2],'rm','/spool/test-ballast')
    s.finish(run,timeout=55);s.assert_records()
    summary['checks'].append({'name':'spool-capacity-recovery','result':'passed','run':run,'observed':'no physical start while full; freeing ballast allowed durable redelivery and completion'})
    print('PASSED spool-capacity-recovery',flush=True)
    print('RUN workflow-input-boundaries',flush=True)
    before=len(request(s.executor+'/runs'))
    def post_error(body):
        try:request(s.executor+'/runs',body)
        except Exception as e:return repr(e)
        raise AssertionError('bad workflow accepted')
    unknown=post_error({'workflow_name':'no-such-workflow'})
    # New invalid workflow definitions are deliberately loaded only into this test container.
    malformed=wf.read_text()+'''\n  - name: Invalid Device\n    steps:\n      - {name: unsupported, device: imaginary-device}\n'''
    bad=s.artifacts/'bad-workflows.yaml';bad.write_text(malformed)
    s.command(['docker','cp',str(bad),executor+':/app/config/workflows.yaml'])
    unsupported=post_error({'workflow_name':'Invalid Device'})
    assert len(request(s.executor+'/runs'))==before
    s.command(['docker','cp',str(wf),executor+':/app/config/workflows.yaml'])
    summary['checks'].append({'name':'workflow-input-boundaries','result':'passed','unknown':unknown,'unsupported':unsupported,'observed':'no run persisted for rejected definitions'})
except BaseException as e:
    summary['failure']=repr(e);summary['traceback']=traceback.format_exc();s.snapshot('failure');print(summary['traceback'],flush=True)
finally:
    summary['cleanup_errors']=s.cleanup()
    for service in ['executor',*DEVICES]:s.command(['docker','image','rm',f'{s.project}-{service}:latest'],check=False)
    summary['seconds']=round(time.time()-summary['started'],2)
    (s.artifacts/'boundaries.json').write_text(json.dumps(summary,indent=2));s.log.close()
    print('FINISHED',s.artifacts,summary.get('failure'),flush=True)
