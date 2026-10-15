#!/bin/sh
set -eu
umask 077

usage() {
  cat <<'USAGE'
用法: scripts/dev.sh [--no-login-helper] [--help]

启动本机 Pixiv Curator（默认 http://127.0.0.1:5722）。私密数据位于 data/dev，
可用 PIXIV_CURATOR_DEV_DIR 覆盖。启动前确保本地 Go 可用；默认会准备 Python 登录依赖。
PIXIV_CURATOR_OPEN_BROWSER=0 可关闭自动打开登录页。
USAGE
}

no_helper=0
for arg in "$@"; do
  case "$arg" in
    --no-login-helper) no_helper=1 ;;
    --help|-h) usage; exit 0 ;;
    *) echo "未知参数: $arg" >&2; usage >&2; exit 2 ;;
  esac
done

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo=$(CDPATH= cd -- "$script_dir/.." && pwd)
cd "$repo"

addr=${PIXIV_CURATOR_ADDR:-127.0.0.1:5722}
base=${PIXIV_CURATOR_PUBLIC_BASE_URL:-http://$addr}
dev_dir=${PIXIV_CURATOR_DEV_DIR:-$repo/data/dev}
case "$dev_dir" in /*) ;; *) dev_dir="$repo/$dev_dir" ;; esac
mkdir -p "$dev_dir"
chmod 700 "$dev_dir"

token_file="$dev_dir/admin-token"
refresh_file="$dev_dir/pixiv-refresh-token"
oauth_file="$dev_dir/oauth.json"
db_file="$dev_dir/curator.db"

# Check bind availability without displaying or reading any credential contents.
python3 - "$addr" <<'PY'
import errno, socket, sys
addr = sys.argv[1]
try:
    host, port = addr.rsplit(':', 1)
    host = host.strip('[]')
    with socket.socket(socket.AF_INET6 if ':' in host else socket.AF_INET, socket.SOCK_STREAM) as s:
        # Match Go net.Listen on Unix: closed connections must not block restart.
        # Do not use SO_REUSEPORT; an active listener must still be rejected.
        s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        s.bind((host, int(port)))
        s.listen(1)
except OSError as exc:
    reason = "已有进程监听或绑定" if exc.errno == errno.EADDRINUSE else "无法绑定，请检查地址与权限"
    print(f"监听地址 {addr}：{reason}（errno={exc.errno}）；未停止任何现有进程。", file=sys.stderr)
    raise SystemExit(1)
except Exception:
    print(f"无效监听地址: {addr}", file=sys.stderr)
    raise SystemExit(1)
PY

python3 - "$token_file" <<'PYTOKEN'
import os, secrets, stat, sys
path = sys.argv[1]
try:
    info = os.lstat(path)
except FileNotFoundError:
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w", encoding="ascii") as output:
        output.write(secrets.token_urlsafe(32) + "\n")
else:
    if not stat.S_ISREG(info.st_mode):
        raise SystemExit("admin token path must be a regular file")
    os.chmod(path, 0o600, follow_symlinks=False)
    if info.st_size == 0:
        with open(path, "w", encoding="ascii") as output:
            output.write(secrets.token_urlsafe(32) + "\n")
PYTOKEN
if [ -e "$refresh_file" ] || [ -L "$refresh_file" ]; then
  python3 - "$refresh_file" <<'PYTOKEN'
import os, stat, sys
path = sys.argv[1]
info = os.lstat(path)
if not stat.S_ISREG(info.st_mode):
    raise SystemExit("refresh token path must be a regular file")
os.chmod(path, 0o600, follow_symlinks=False)
PYTOKEN
fi

if [ "$no_helper" -eq 0 ]; then
  req="$repo/scripts/requirements-login.txt"
  helper="$repo/scripts/pixiv-login.py"
  [ -f "$req" ] || { echo "缺少锁定依赖文件: $req" >&2; exit 1; }
  [ -f "$helper" ] || { echo "缺少登录 helper: $helper" >&2; exit 1; }
  venv="$dev_dir/venv"
  export PLAYWRIGHT_BROWSERS_PATH="$dev_dir/playwright-browsers"
  if [ ! -x "$venv/bin/python" ] || ! "$venv/bin/python" -m pip --version >/dev/null 2>&1; then
    rm -rf "$venv"
    python3 -m venv "$venv"
  fi
  req_hash=$(python3 - "$req" <<'PY'
import hashlib, sys
print(hashlib.sha256(open(sys.argv[1], 'rb').read()).hexdigest())
PY
)
  marker="$venv/.requirements-login.sha256"
  installed_hash=
  if [ -r "$marker" ]; then installed_hash=$(cat "$marker"); fi
  if [ "$installed_hash" != "$req_hash" ] || ! "$venv/bin/python" -c 'import importlib.metadata, gppt; assert importlib.metadata.version("gppt") == "5.1.0"' >/dev/null 2>&1 || ! "$venv/bin/python" -m pip check >/dev/null 2>&1; then
    rm -f "$marker"
    "$venv/bin/python" -m pip install --disable-pip-version-check -r "$req"
    "$venv/bin/python" -c 'import gppt, playwright; import importlib.metadata; assert importlib.metadata.version("gppt") == "5.1.0"'
    "$venv/bin/python" -m pip check
    marker_tmp="$marker.tmp.$$"
    printf '%s\n' "$req_hash" > "$marker_tmp"
    chmod 600 "$marker_tmp"
    mv -f "$marker_tmp" "$marker"
  fi
  "$venv/bin/python" "$helper" configure "$oauth_file"
  unset PIXIV_CURATOR_CALLBACK_DIR
  if [ "${PIXIV_CURATOR_LOCAL_CALLBACK:-1}" = 1 ] && [ "$(uname -s)" = Linux ] && [ -n "${DISPLAY:-}${WAYLAND_DISPLAY:-}" ] && command -v xdg-mime >/dev/null 2>&1; then
    if "$venv/bin/python" "$repo/scripts/pixiv-callback.py" install "$dev_dir/callback"; then
      export PIXIV_CURATOR_CALLBACK_DIR="$dev_dir/callback"
      echo "已启用本机自动回调：登录后允许浏览器打开 Pixiv Curator Login。"
    else
      echo "本机回调未启用，仍可使用手动授权码方式。" >&2
    fi
  fi
  export PIXIV_CURATOR_LOGIN_PYTHON="$venv/bin/python"
  export PIXIV_CURATOR_LOGIN_HELPER="$helper"
fi

export PIXIV_CURATOR_ADDR="$addr"
export PIXIV_CURATOR_PUBLIC_BASE_URL="$base"
export PIXIV_CURATOR_DATABASE_PATH="$db_file"
export PIXIV_CURATOR_ADMIN_TOKEN_FILE="$token_file"
export PIXIV_CURATOR_PIXIV_TOKEN_FILE="$refresh_file"
if [ "$no_helper" -eq 0 ]; then
  export PIXIV_CURATOR_OAUTH_CONFIG_FILE="$oauth_file"
else
  unset PIXIV_CURATOR_OAUTH_CONFIG_FILE
  unset PIXIV_CURATOR_LOGIN_PYTHON PIXIV_CURATOR_LOGIN_HELPER
fi

mkdir -p "$repo/bin"
go build -trimpath -o "$repo/bin/pixiv-curator" ./cmd/api
"$repo/bin/pixiv-curator" &
server_pid=$!
cleanup() {
  kill "$server_pid" 2>/dev/null || true
  wait "$server_pid" 2>/dev/null || true
}
trap cleanup EXIT HUP INT TERM

url="$base/admin/login"
ready=0
i=0
while [ "$i" -lt 60 ]; do
  if ! kill -0 "$server_pid" 2>/dev/null; then
    echo "服务进程已退出。" >&2
    exit 1
  fi
  if python3 - "$base/readyz" <<'PY' >/dev/null 2>&1
import sys, urllib.request
try:
    with urllib.request.urlopen(sys.argv[1], timeout=1) as r:
        raise SystemExit(0 if r.status == 200 else 1)
except Exception:
    raise SystemExit(1)
PY
  then ready=1; break; fi
  i=$((i + 1))
  sleep 1
done
if [ "$ready" -ne 1 ]; then
  echo "等待 /readyz 超时。" >&2
  exit 1
fi
printf '服务已就绪: %s\n登录页: %s\n管理 token 文件: %s\n' "$base" "$url" "$token_file"
if [ "${PIXIV_CURATOR_OPEN_BROWSER:-1}" != 0 ]; then
  if command -v xdg-open >/dev/null 2>&1; then xdg-open "$url" >/dev/null 2>&1 || true
  elif command -v open >/dev/null 2>&1; then open "$url" >/dev/null 2>&1 || true
  fi
fi
wait "$server_pid"
