"""Load both native plugins in disposable CPA; no Google credentials or host ports."""
import json,pathlib,secrets,subprocess,tempfile,time,urllib.request,urllib.error,sys,sqlite3
dist=pathlib.Path(__file__).resolve().parents[1]/'dist'
image=sys.argv[1] if len(sys.argv)>1 else 'codex-cpa:v8.0.67-headfix.1'
def docker(*args):return subprocess.check_output(['docker',*args],text=True,stderr=subprocess.PIPE).strip()
with tempfile.TemporaryDirectory(prefix='cpa-identity-smoke-') as temp:
 root=pathlib.Path(temp);plugins=root/'plugins/linux/amd64';plugins.mkdir(parents=True)
 for src,name in [('dadahhhh-gemini-web-v021.so','dadahhhh-gemini-web.so'),('web-gemini-identity.so','web-gemini-identity.so')]:
  (plugins/name).write_bytes((dist/src).read_bytes())
 key=secrets.token_hex(24)
 identity={'enabled':True,'chrome_version':'157.0.8103.40'}
 config={'config-version':8,'server':{'host':'0.0.0.0','port':8317},'access':{'api-keys':[key]},'management':{'allow-remote':True,'secret-key':key,'disable-control-panel':True},'oauth':{'auth-dir':'/state/empty-auths'},'plugins':{'enabled':True,'dir':'/state/plugins','configs':{'dadahhhh-gemini-web':{'enabled':True,'data_dir':'/state/web-data','guest_enabled':False},'web-gemini-identity':identity}}}
 path=root/'config.json';path.write_text(json.dumps(config));path.chmod(0o600);cid=None
 try:
  cid=docker('create','--name','codex-web-identity-smoke','--restart','no','--memory','1g','--mount',f'type=bind,source={root},target=/state',image,'./CLIProxyAPI','-config','/state/config.json')
  docker('start',cid);info=json.loads(docker('inspect',cid))[0];base='http://'+info['NetworkSettings']['IPAddress']+':8317'
  def request(route,body=None):
   req=urllib.request.Request(base+route,data=json.dumps(body).encode() if body is not None else None,headers={'Authorization':'Bearer '+key,'Content-Type':'application/json'})
   try:
    with urllib.request.urlopen(req,timeout=10) as r:return r.status,json.loads(r.read())
   except urllib.error.HTTPError as e:
    raw=e.read()
    try:return e.code,json.loads(raw)
    except:return e.code,{}
  statusRoute='/v0/management/plugins/web-gemini-identity/status';webRoute='/v0/management/plugins/dadahhhh-gemini-web/status'
  for _ in range(40):
   try:
    status,state=request(statusRoute)
    if status==200:break
   except OSError:pass
   time.sleep(.5)
  else:raise AssertionError('identity plugin failed to load')
  before=state['before_calls']+state['after_calls']
  code,_=request('/v1/chat/completions',{'model':'gemini-web/gemini-4-hydrogen','messages':[{'role':'user','content':'synthetic local test'}]})
  assert code in (400,503),code
  _,state=request(statusRoute);_,web=request(webRoute)
  assert state['before_calls']+state['after_calls']>before,'CPA interceptor did not run'
  assert web['identity_policy_applications']>0,'Policy did not reach embedded executor'
  applied=web['identity_policy_applications'];calls=state['before_calls']+state['after_calls']
  request('/v1/chat/completions',{'model':'gemini-3.8-flash-high','messages':[{'role':'user','content':'synthetic non-web test'}]})
  _,state=request(statusRoute);assert state['before_calls']+state['after_calls']==calls,'Non-web model was intercepted'
  identity['enabled']=False;path.write_text(json.dumps(config))
  for _ in range(40):
   status,state=request(statusRoute)
   if status in (404,503) or state.get('enabled') is False:break
   time.sleep(.25)
  else:raise AssertionError('Identity disable did not hot reload')
  status,web=request(webRoute);assert status==200
  disabledBaseline=web['identity_policy_applications']
  request('/v1/chat/completions',{'model':'gemini-web/gemini-4-hydrogen','messages':[{'role':'user','content':'synthetic disabled test'}]})
  _,web=request(webRoute);assert web['identity_policy_applications']==disabledBaseline,'Disabled identity policy still ran'
  time.sleep(.5)
  ledger=sqlite3.connect('file:'+str(root/'web-data/requests.db')+'?mode=ro',uri=True)
  count=ledger.execute('select count(*) from requests').fetchone()[0];ledger.close()
  assert count>=2,'Identity request accounting was lost'
  print(json.dumps({'pass':True,'bothNativePluginsLoaded':True,'policyReachedExecutor':True,'nonWebUnaffected':True,'independentDisableVerified':True,'requestRecords':count,'googleRequestsSent':False,'hostPortsPublished':False}))
 finally:
  if cid:docker('rm','-f',cid)
