"""Run with Docker access on Linux; creates one disposable, credential-free CPA."""
import json
import pathlib
import secrets
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

image = sys.argv[1] if len(sys.argv) > 1 else 'codex-cpa:v8.0.67-headfix.1'
dist = (pathlib.Path(__file__).resolve().parents[1] / 'dist').resolve()
name = 'codex-gemini-web-plugin-smoke'
plugin_id = 'dadahhhh-gemini-web'

def docker(*args):
    return subprocess.check_output(['docker', *args], text=True, stderr=subprocess.PIPE).strip()

with tempfile.TemporaryDirectory(prefix='cpa-web-smoke-') as data:
    key = secrets.token_hex(24)
    config = {'config-version': 8, 'server': {'host': '0.0.0.0', 'port': 8317},
              'access': {'api-keys': [key]},
              'management': {'allow-remote': True, 'secret-key': key, 'disable-control-panel': True},
              'oauth': {'auth-dir': '/state/empty-auths'},
              'plugins': {'enabled': True, 'dir': '/plugin', 'configs': {
                  plugin_id: {'enabled': True, 'data_dir': '/state/plugin-data', 'guest_enabled': False}}}}
    path = pathlib.Path(data) / 'config.json'
    path.write_text(json.dumps(config))
    path.chmod(0o600)
    cid = None
    try:
        cid = docker('create', '--name', name, '--restart', 'no', '--memory', '768m',
                     '--mount', f'type=bind,source={dist},target=/plugin/linux/amd64,readonly',
                     '--mount', f'type=bind,source={data},target=/state',
                     image, './CLIProxyAPI', '-config', '/state/config.json')
        docker('start', cid)
        info = json.loads(docker('inspect', cid))[0]
        base = 'http://' + info['NetworkSettings']['IPAddress'] + ':8317'

        def request(path, method='GET', body=None, auth=True):
            headers = {'Content-Type': 'application/json'}
            if auth:
                headers['Authorization'] = 'Bearer ' + key
            req = urllib.request.Request(base + path, method=method, headers=headers,
                                         data=json.dumps(body).encode() if body is not None else None)
            try:
                with urllib.request.urlopen(req, timeout=15) as r:
                    return r.status, r.read()
            except urllib.error.HTTPError as e:
                return e.code, e.read()

        for _ in range(30):
            try:
                if request('/')[0] == 200:
                    break
            except OSError:
                pass
            time.sleep(.5)
        else:
            raise RuntimeError('isolated CPA did not start')
        for _ in range(20):
            models = json.loads(request('/v1/models')[1])['data']
            if models:
                break
            time.sleep(.25)
        assert models and all(m['id'].startswith('gemini-web/') for m in models), models
        resource = f'/v0/resource/plugins/{plugin_id}/accounts'
        assert request(resource, auth=False)[0] == 200
        management = f'/v0/management/plugins/{plugin_id}/status'
        assert request(management, auth=False)[0] == 401
        status, body = request(management)
        assert status == 200 and json.loads(body)['total'] == 0, (status, body)
        body = {'model': 'gemini-web/gemini-3.8-flash', 'messages': [{'role': 'user', 'content': 'hi'}]}
        status, _ = request('/v1/chat/completions', 'POST', body)
        assert status in (400, 503), status
        assert 'plugin registered plugin_id=' + plugin_id in docker('logs', cid)
        print(json.dumps({'pass': True, 'image': image, 'plugin_loaded': True,
                          'models': len(models), 'public_resource': 200,
                          'unauthenticated_management': 401, 'authenticated_management': 200,
                          'no_account_http_status': status, 'host_ports_published': False}))
    except Exception:
        if cid:
            print(subprocess.check_output(['docker', 'logs', cid], text=True, stderr=subprocess.STDOUT)[-7000:])
        raise
    finally:
        if cid:
            docker('rm', '-f', cid)
