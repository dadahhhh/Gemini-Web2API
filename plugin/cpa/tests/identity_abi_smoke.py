"""Credential-free ABI checks for the independently loaded identity plugin."""
import base64,ctypes as C,json,pathlib,sys
class Buffer(C.Structure):_fields_=[('ptr',C.c_void_p),('length',C.c_size_t)]
CALL=C.CFUNCTYPE(C.c_int,C.c_char_p,C.c_void_p,C.c_size_t,C.POINTER(Buffer));FREE=C.CFUNCTYPE(None,C.c_void_p,C.c_size_t);SHUTDOWN=C.CFUNCTYPE(None)
class API(C.Structure):_fields_=[('version',C.c_uint32),('call',CALL),('free',FREE),('shutdown',SHUTDOWN)]
lib=C.CDLL(str(pathlib.Path(sys.argv[1]).resolve()));lib.cliproxy_plugin_init.argtypes=[C.c_void_p,C.POINTER(API)]
api=API();assert lib.cliproxy_plugin_init(None,C.byref(api))==0 and api.version==1
def call(method,payload):
 raw=json.dumps(payload).encode();buf=C.create_string_buffer(raw);out=Buffer();api.call(method.encode(),buf,len(raw),C.byref(out))
 try:return json.loads(C.string_at(out.ptr,out.length))
 finally:api.free(out.ptr,out.length)
try:
 reg=call('plugin.register',{'config_yaml':base64.b64encode(b'enabled: true\n').decode()});assert reg['ok']
 assert reg['result']['metadata']['Name']=='web-gemini-identity'
 assert reg['result']['capabilities']['request_interceptor']
 for model,want in [('gemini-web/gemini-4-hydrogen',True),('web/gemini-4-argon',True),('gemini-3.8-flash-high',False),('gpt-6.1-sol',False),('claude-sonnet-5-5',False)]:
  response=call('request.intercept_after',{'Model':model})
  assert response['ok'];headers=response['result'].get('Headers') or {}
  assert bool(headers)==want
  if want:
   profile=json.loads(next(iter(headers.values()))[0]);assert profile['version']==1 and 'Chrome/157.' in profile['headers']['user-agent']
 print('PASS: identity native ABI, isolated scope, browser profile and buffer release')
finally:api.shutdown()
