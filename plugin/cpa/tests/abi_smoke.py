"""No credentials or internet: exercise the actual exported C ABI with ctypes."""
import base64
import ctypes as C
import json
import pathlib
import sys
import tempfile

class Buffer(C.Structure):
    _fields_ = [('ptr', C.c_void_p), ('length', C.c_size_t)]

CALL = C.CFUNCTYPE(C.c_int, C.c_char_p, C.c_void_p, C.c_size_t, C.POINTER(Buffer))
FREE = C.CFUNCTYPE(None, C.c_void_p, C.c_size_t)
SHUTDOWN = C.CFUNCTYPE(None)

class API(C.Structure):
    _fields_ = [('version', C.c_uint32), ('call', CALL), ('free', FREE), ('shutdown', SHUTDOWN)]

lib = C.CDLL(str(pathlib.Path(sys.argv[1]).resolve()))
lib.cliproxy_plugin_init.argtypes = [C.c_void_p, C.POINTER(API)]
lib.cliproxy_plugin_init.restype = C.c_int
api = API()
assert lib.cliproxy_plugin_init(None, C.byref(api)) == 0 and api.version == 1

def invoke(method, payload):
    raw = json.dumps(payload).encode()
    source = C.create_string_buffer(raw)
    out = Buffer()
    api.call(method.encode(), source, len(raw), C.byref(out))
    try:
        return json.loads(C.string_at(out.ptr, out.length))
    finally:
        api.free(out.ptr, out.length)

with tempfile.TemporaryDirectory() as data:
    config = f'data_dir: {json.dumps(data)}\nguest_enabled: false\n'
    registered = invoke('plugin.register', {'config_yaml': base64.b64encode(config.encode()).decode()})
    assert registered['ok'], registered
    assert registered['result']['schema_version'] == 10
    models = invoke('model.register', {})
    assert models['ok'] and models['result']['Models']
    assert all(m['ID'].startswith('gemini-web/') for m in models['result']['Models'])
    assert invoke('model.route', {'RequestedModel': 'gemini-web/gemini-3.8-flash'})['result']['Handled']
    assert not invoke('model.route', {'RequestedModel': 'gemini-3.8-flash-high'})['result']['Handled']
    # No accounts and guest disabled: surface a real error instead of fake model output.
    payload = base64.b64encode(b'{"messages":[{"role":"user","content":"hi"}]}').decode()
    failed = invoke('executor.execute', {'Model': 'gemini-web/gemini-3.8-flash', 'Format': 'openai', 'Payload': payload})
    assert not failed['ok'] and failed['error']['http_status'] in (400, 503), failed
    api.shutdown()
print('PASS: C ABI registration, model routing, no-account error, buffer freeing and shutdown')
