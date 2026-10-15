#!/usr/bin/env python3
"""Offline bridge tests; no gppt installation, browser or Pixiv account needed."""
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import sys
import tempfile
from types import ModuleType, SimpleNamespace
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('login_bridge', Path(__file__).with_name('pixiv-login.py'))
bridge = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bridge)


def modules():
    gppt = ModuleType('gppt')
    gppt.browser = ModuleType('gppt.browser')
    gppt.token = ModuleType('gppt.token')
    gppt.consts = ModuleType('gppt.consts')
    gppt.browser.BROWSER_ARGS = ['--no-sandbox', '--disable-gpu']
    gppt.consts.CLIENT_ID = 'test-client'
    gppt.consts.CLIENT_SECRET = 'test-app-secret'
    gppt.login = lambda **kw: SimpleNamespace(refresh_token='test-refresh')
    return gppt, {'gppt': gppt, 'gppt.browser': gppt.browser, 'gppt.token': gppt.token, 'gppt.consts': gppt.consts}


class BridgeTests(unittest.TestCase):
    def test_configure_private_preserves_matching_and_rejects_overwrite(self):
        gppt, mods = modules()
        with tempfile.TemporaryDirectory() as directory, patch.dict(sys.modules, mods), patch.object(bridge, 'dependencies', return_value=gppt):
            os.chmod(directory, 0o700)
            target = Path(directory) / 'oauth.json'
            bridge.configure(target)
            original = target.read_bytes()
            bridge.configure(target)
            self.assertEqual(target.read_bytes(), original)
            if os.name != 'nt': self.assertEqual(target.stat().st_mode & 0o777, 0o600)
            target.write_text('{"client_id":"different"}')
            with self.assertRaisesRegex(bridge.BridgeError, 'OAUTH_CONFIG_MISMATCH'): bridge.configure(target)
            self.assertEqual(target.read_text(), '{"client_id":"different"}')

    def test_login_uses_manual_browser_and_silences_library(self):
        gppt, mods = modules()
        def login(**kw):
            self.assertEqual(kw, {'headless': False})
            print('sensitive-upstream-log')
            print('sensitive-error', file=sys.stderr)
            return SimpleNamespace(refresh_token='test-refresh')
        gppt.login = login
        out, err = io.StringIO(), io.StringIO()
        with patch.dict(sys.modules, mods), patch.dict(os.environ, {'DISPLAY': ':test'}), patch.object(bridge, 'dependencies', return_value=gppt), contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            self.assertEqual(bridge.login(), {'refresh_token': 'test-refresh'})
        self.assertEqual(out.getvalue(), '')
        self.assertEqual(err.getvalue(), '')
        self.assertNotIn('--no-sandbox', gppt.browser.BROWSER_ARGS)
        self.assertIs(gppt.browser.install, bridge.installed_only)

    def test_browser_pkce_uses_no_automation(self):
        gppt, mods = modules()
        oauth = ModuleType('gppt.oauth')
        oauth.generate_pkce = lambda: ('private-verifier', 'public-challenge')
        oauth.build_login_url = lambda challenge: 'https://app-api.pixiv.net/web/v1/login?code_challenge=' + challenge
        oauth.extract_code = lambda value: value
        oauth.Authorization = lambda **kw: SimpleNamespace(**kw)
        mods['gppt.oauth'] = oauth
        def exchange(auth):
            self.assertEqual(auth.code, 'sample-code')
            self.assertEqual(auth.code_verifier, 'private-verifier')
            print('private upstream output')
            return SimpleNamespace(refresh_token='test-refresh')
        gppt.token.exchange = exchange
        out = io.StringIO()
        with patch.dict(sys.modules, mods), patch.object(bridge, 'dependencies', return_value=gppt), patch.object(sys, 'stdin', io.StringIO('{"code":"sample-code"}\n')), contextlib.redirect_stdout(out):
            self.assertEqual(bridge.browser_login(), {'refresh_token': 'test-refresh'})
        self.assertEqual(list(json.loads(out.getvalue())), ['login_url'])
        self.assertNotIn('private', out.getvalue())

    def test_errors_do_not_expose_upstream_payload(self):
        out = io.StringIO()
        with patch.object(sys, 'argv', ['helper', 'login']), patch.object(bridge, 'login', side_effect=RuntimeError('private-token-value')), contextlib.redirect_stdout(out):
            self.assertEqual(bridge.main(), 1)
        self.assertEqual(json.loads(out.getvalue()), {'error_code': 'LOGIN_FAILED'})

    def test_token_endpoint_redirects_rejected(self):
        requests = ModuleType('requests')
        def post(url, **kwargs):
            self.assertFalse(kwargs['allow_redirects'])
            return SimpleNamespace(status_code=307)
        requests.post = post
        with patch.dict(sys.modules, {'requests': requests}):
            with self.assertRaises(bridge.BridgeError): bridge.guarded_post('https://oauth.secure.pixiv.net/auth/token')
            with self.assertRaises(bridge.BridgeError): bridge.guarded_post('https://untrusted.example/')

    def test_missing_browser_never_installs(self):
        with self.assertRaisesRegex(bridge.BridgeError, 'LOGIN_BROWSER_UNAVAILABLE'):
            bridge.installed_only([SimpleNamespace(executable_path='/nonexistent/curator-test-browser')], with_deps=True)


if __name__ == '__main__':
    unittest.main()
