#!/usr/bin/env python3
"""Private subprocess bridge to gppt 5.1.0; never print upstream diagnostics."""
import contextlib
import importlib.metadata
import importlib.util
import json
import os
from pathlib import Path
import stat
import sys
from types import SimpleNamespace


class BridgeError(Exception):
    pass


def dependencies():
    if importlib.metadata.version('gppt') != '5.1.0':
        raise BridgeError('LOGIN_DEPENDENCY_UNAVAILABLE')
    import gppt
    return gppt


def configure(path):
    dependencies()
    from gppt.consts import CLIENT_ID, CLIENT_SECRET
    expected = {'client_id': CLIENT_ID, 'client_secret': CLIENT_SECRET}
    target = Path(path)
    parent = target.parent.stat()
    if not stat.S_ISDIR(parent.st_mode) or (os.name != 'nt' and parent.st_mode & 0o077):
        raise BridgeError('PRIVATE_DIRECTORY_REQUIRED')
    if target.exists() or target.is_symlink():
        info = target.lstat()
        if not stat.S_ISREG(info.st_mode) or (os.name != 'nt' and info.st_mode & 0o077):
            raise BridgeError('PRIVATE_FILE_REQUIRED')
        existing = json.loads(target.read_text())
        if any(existing.get(key) != value for key, value in expected.items()):
            raise BridgeError('OAUTH_CONFIG_MISMATCH')
        return
    # Exclusive creation preserves an existing configuration, even under a race.
    fd = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'w') as output:
        json.dump(expected, output)
        output.write('\n')
        output.flush()
        os.fsync(output.fileno())


def installed_only(browsers, **_kwargs):
    if not all(Path(browser.executable_path).is_file() for browser in browsers):
        raise BridgeError('LOGIN_BROWSER_UNAVAILABLE')


def guarded_post(url, **kwargs):
    if url != 'https://oauth.secure.pixiv.net/auth/token':
        raise BridgeError('LOGIN_FAILED')
    import requests
    kwargs['allow_redirects'] = False
    result = requests.post(url, **kwargs)
    if result.status_code != 200:
        raise BridgeError('LOGIN_FAILED')
    return result


@contextlib.contextmanager
def sandboxed_playwright():
    from playwright.sync_api import sync_playwright
    with sync_playwright() as pw:
        def launch(**kwargs):
            return pw.chromium.launch(chromium_sandbox=True, **kwargs)
        yield SimpleNamespace(chromium=SimpleNamespace(executable_path=pw.chromium.executable_path, launch=launch))


def login():
    gppt = dependencies()
    if sys.platform.startswith('linux') and not (os.getenv('DISPLAY') or os.getenv('WAYLAND_DISPLAY')):
        raise BridgeError('LOGIN_DISPLAY_UNAVAILABLE')
    import gppt.browser
    import gppt.token
    # Installation belongs to the launcher, never a privileged operation during login.
    gppt.browser.install = installed_only
    gppt.browser.sync_playwright = sandboxed_playwright
    gppt.browser.BROWSER_ARGS = [arg for arg in gppt.browser.BROWSER_ARGS if arg != '--no-sandbox']
    gppt.token.requests = SimpleNamespace(post=guarded_post)
    with open(os.devnull, 'w') as sink, contextlib.redirect_stdout(sink), contextlib.redirect_stderr(sink):
        issued = gppt.login(headless=False)
    value = issued.refresh_token
    if not isinstance(value, str) or not value or len(value) > 16384 or any(c.isspace() for c in value):
        raise BridgeError('LOGIN_FAILED')
    return {'refresh_token': value}


def browser_login():
    dependencies()
    from gppt.oauth import generate_pkce, build_login_url, extract_code, Authorization
    import gppt.token
    gppt.token.requests = SimpleNamespace(post=guarded_post)
    verifier, challenge = generate_pkce()
    directory = os.getenv('PIXIV_CURATOR_CALLBACK_DIR') if sys.platform.startswith('linux') else None
    if directory:
        spec = importlib.util.spec_from_file_location('pixiv_callback', Path(__file__).with_name('pixiv-callback.py'))
        callback = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(callback)
        with callback.receiver(directory) as server:
            print(json.dumps({'login_url': build_login_url(challenge)}), flush=True)
            raw = callback.wait_code(server, sys.stdin)
    else:
        print(json.dumps({'login_url': build_login_url(challenge)}), flush=True)
        raw = sys.stdin.readline(16385)
    if len(raw) > 16384:
        raise BridgeError('LOGIN_FAILED')
    supplied = json.loads(raw).get('code', '')
    if not isinstance(supplied, str):
        raise BridgeError('LOGIN_FAILED')
    code = extract_code(supplied)
    with open(os.devnull, 'w') as sink, contextlib.redirect_stdout(sink), contextlib.redirect_stderr(sink):
        issued = gppt.token.exchange(Authorization(code=code, code_verifier=verifier))
    return {'refresh_token': issued.refresh_token}


def main():
    os.umask(0o077)
    try:
        if len(sys.argv) == 3 and sys.argv[1] == 'configure':
            configure(sys.argv[2])
            return 0
        if sys.argv[1:] == ['browser-login']:
            json.dump(browser_login(), sys.stdout)
            return 0
        if sys.argv[1:] == ['login']:
            # Go receives this through a bounded private pipe, never the HTTP response.
            result = login()
            json.dump(result, sys.stdout)
            return 0
        raise BridgeError('LOGIN_INVALID_COMMAND')
    except BridgeError as exc:
        print(json.dumps({'error_code': str(exc)}))
    except (ImportError, importlib.metadata.PackageNotFoundError):
        print(json.dumps({'error_code': 'LOGIN_DEPENDENCY_UNAVAILABLE'}))
    except Exception:
        # Third-party errors may contain URLs, authorization codes or token bodies.
        print(json.dumps({'error_code': 'LOGIN_FAILED'}))
    return 1


if __name__ == '__main__':
    sys.exit(main())
