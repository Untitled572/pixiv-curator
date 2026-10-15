#!/usr/bin/env python3
"""Linux user-level pixiv URI receiver; credentials never go to disk or logs."""
import contextlib
import json
import os
from pathlib import Path
import queue
import socket
import stat
import subprocess
import sys
import threading
from urllib.parse import urlsplit, parse_qs


def private_dir(directory):
    path = Path(directory)
    path.mkdir(mode=0o700, parents=True, exist_ok=True)
    info = path.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
        raise ValueError('private directory required')
    return path


def callback(directory, uri):
    parsed = urlsplit(uri)
    if len(uri) > 8192 or parsed.scheme != 'pixiv' or parsed.netloc != 'account' or parsed.path != '/login' or parsed.fragment:
        raise ValueError('invalid callback')
    codes = parse_qs(parsed.query).get('code', [])
    if len(codes) != 1 or not codes[0] or any(c.isspace() for c in codes[0]):
        raise ValueError('invalid code')
    path = private_dir(directory) / 'login.sock'
    with socket.socket(socket.AF_UNIX) as client:
        client.settimeout(3)
        client.connect(str(path))
        client.sendall(json.dumps({'code': codes[0]}).encode() + b'\n')


@contextlib.contextmanager
def receiver(directory):
    import fcntl
    path = private_dir(directory)
    with open(path / 'login.lock', 'a') as lock:
        os.chmod(path / 'login.lock', 0o600)
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        address = path / 'login.sock'
        address.unlink(missing_ok=True)
        with socket.socket(socket.AF_UNIX) as server:
            server.bind(str(address)); os.chmod(address, 0o600); server.listen(2)
            try:
                yield server
            finally:
                address.unlink(missing_ok=True)


def wait_code(server, stdin):
    results = queue.Queue()
    def read_manual():
        raw = stdin.readline(16385)
        if raw: results.put(raw)
    def read_callback():
        while True:
            try:
                client, _ = server.accept()
                with client:
                    client.settimeout(2)
                    raw = client.makefile('rb').readline(16385)
                    if len(raw) <= 16384 and raw.endswith(b'\n'):
                        results.put(raw.decode()); return
            except (OSError, UnicodeError):
                if server.fileno() < 0: return
    threading.Thread(target=read_manual, daemon=True).start()
    threading.Thread(target=read_callback, daemon=True).start()
    return results.get(timeout=360)


def install(directory):
    private_dir(directory)
    name = 'pixiv-curator-login.desktop'
    current = subprocess.check_output(['xdg-mime', 'query', 'default', 'x-scheme-handler/pixiv'], text=True).strip()
    if current and current != name:
        raise ValueError('another application owns pixiv protocol')
    def quote(value):
        # Desktop Exec escaping; never invoke a shell. Reject field-code expansion.
        if any(c in value for c in ['%', '\n', '\r']): raise ValueError('unsupported path')
        return '"' + ''.join('\\' + c if c in '\\"`$' else c for c in value).replace('\\', '\\\\') + '"'
    apps = Path(os.getenv('XDG_DATA_HOME', str(Path.home()/'.local/share'))) / 'applications'
    apps.mkdir(parents=True, exist_ok=True)
    command = ' '.join(quote(v) for v in [sys.executable, str(Path(__file__).resolve()), 'callback', str(Path(directory).resolve())]) + ' %u'
    (apps/name).write_text('[Desktop Entry]\nType=Application\nName=Pixiv Curator Login\nNoDisplay=true\nTerminal=false\nExec='+command+'\nMimeType=x-scheme-handler/pixiv;\n')
    subprocess.run(['xdg-mime','default',name,'x-scheme-handler/pixiv'], check=True)
    if subprocess.check_output(['xdg-mime','query','default','x-scheme-handler/pixiv'],text=True).strip()!=name:
        raise ValueError('registration failed')


if __name__ == '__main__':
    os.umask(0o077)
    try:
        if sys.argv[1] == 'install' and len(sys.argv)==3: install(sys.argv[2])
        elif sys.argv[1] == 'callback' and len(sys.argv)==4: callback(sys.argv[2], sys.argv[3])
        else: raise ValueError('invalid command')
    except Exception:
        print('Pixiv 回调未接收：请确认登录正在进行，或使用管理页手动回填。', file=sys.stderr)
        sys.exit(1)
