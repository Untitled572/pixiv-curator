"""Offline native Windows integration checks for scripts/dev.ps1.

Run with: py -3 scripts/test-windows-launcher.py
The suite uses a temporary DevDir and loopback ports; it never contacts Pixiv.
"""

from __future__ import annotations

import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import time
import unittest
import urllib.error
import urllib.request


ROOT = Path(__file__).resolve().parents[1]
LAUNCHER = ROOT / "scripts" / "dev.ps1"
IS_WINDOWS = os.name == "nt"


def powershell_executable() -> str | None:
    return os.environ.get("PIXIV_CURATOR_TEST_POWERSHELL") or shutil.which("pwsh") or shutil.which("powershell") or shutil.which("powershell.exe")


def free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
        listener.bind(("127.0.0.1", 0))
        return int(listener.getsockname()[1])


def port_is_free(port: int) -> bool:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
        try:
            listener.bind(("127.0.0.1", port))
        except OSError:
            return False
        return True


def http_status(url: str, token: str | None = None, timeout: float = 1.5) -> int:
    headers = {"Authorization": "Bearer " + token} if token is not None else {}
    request = urllib.request.Request(url, headers=headers)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        with opener.open(request, timeout=timeout) as response:
            return response.status
    except urllib.error.HTTPError as error:
        return error.code


@unittest.skipUnless(IS_WINDOWS, "Windows 原生启动器集成验收仅在 Windows 运行")
class WindowsLauncherTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.powershell = powershell_executable()
        if not cls.powershell:
            raise unittest.SkipTest("未找到 PowerShell 或 pwsh")
        if not LAUNCHER.is_file():
            raise unittest.SkipTest("scripts/dev.ps1 尚未提供")

    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory(prefix="Pixiv Curator 离线验收-")
        self.dev_dir = Path(self.temp.name) / "私密 测试目录"
        self.port = free_port()
        self.address = f"127.0.0.1:{self.port}"
        self.env = os.environ.copy()
        self.env.update(
            PIXIV_CURATOR_DEV_DIR=str(self.dev_dir),
            PIXIV_CURATOR_ADDR=self.address,
            PIXIV_CURATOR_OPEN_BROWSER="0",
        )
        self.env.pop("PIXIV_CURATOR_OAUTH_CONFIG_FILE", None)
        self.env.pop("PIXIV_CURATOR_ADMIN_TOKEN_FILE", None)
        self.env.pop("PIXIV_CURATOR_DATABASE_PATH", None)

    def tearDown(self) -> None:
        self.temp.cleanup()

    def command(self, *args: str, launcher: Path | None = None) -> list[str]:
        return [
            self.powershell,
            "-NoLogo",
            "-NoProfile",
            "-NonInteractive",
            "-ExecutionPolicy",
            "Bypass",
            "-File",
            str(launcher or LAUNCHER),
            *args,
        ]

    def start_launcher(self, *extra_args: str, no_login_helper: bool = True) -> subprocess.Popen[str]:
        args = ["-Address", self.address, "-DevDir", str(self.dev_dir), "-NoBrowser"]
        if no_login_helper:
            args.append("-NoLoginHelper")
        args.extend(extra_args)
        return subprocess.Popen(
            self.command(*args),
            cwd=ROOT,
            env=self.env,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            encoding="utf-8",
            errors="replace",
            creationflags=getattr(subprocess, "CREATE_NEW_PROCESS_GROUP", 0),
        )

    def wait_ready(self, process: subprocess.Popen[str]) -> None:
        deadline = time.monotonic() + 45
        while time.monotonic() < deadline:
            if process.poll() is not None:
                output, _ = process.communicate(timeout=2)
                diagnostic = self.redact(output).strip()
                self.fail("启动器在服务就绪前退出。诊断输出（敏感值已脱敏）：\n" + diagnostic)
            try:
                if http_status(f"http://{self.address}/readyz") == 200:
                    return
            except (OSError, TimeoutError, urllib.error.URLError):
                pass
            time.sleep(0.25)
        self.fail("等待 /readyz 超时")

    def redact(self, output: str) -> str:
        secrets_to_hide = ["offline-test-secret", "preexisting-offline-admin-token-value"]
        for path in (self.token_file(), self.dev_dir / "oauth.json"):
            try:
                raw = path.read_text(encoding="utf-8").strip()
            except (OSError, UnicodeError):
                continue
            if raw:
                secrets_to_hide.append(raw)
                if path.name == "oauth.json":
                    try:
                        import json

                        config = json.loads(raw)
                        secrets_to_hide.extend(str(value) for key, value in config.items() if "secret" in key.lower() and value)
                    except (ValueError, AttributeError):
                        pass
        for secret in secrets_to_hide:
            output = output.replace(secret, "[REDACTED]")
        return output

    def token_file(self) -> Path:
        # Match scripts/dev.sh's stable per-dev-directory credential filename.
        return self.dev_dir / "admin-token"

    def token_value(self) -> str:
        path = self.token_file()
        self.assertTrue(path.is_file(), "启动器未生成管理 token 文件")
        token = path.read_text(encoding="ascii").strip()
        self.assertTrue(token, "管理 token 文件为空")
        return token

    def assert_private_acl(self, path: Path) -> None:
        result = subprocess.run(
            ["icacls", str(path)], capture_output=True, text=True, encoding="utf-8", errors="replace", timeout=10
        )
        self.assertEqual(result.returncode, 0, "无法检查 Windows ACL")
        acl = result.stdout.lower()
        # The file may be granted to its owner, SYSTEM, and local Administrators.
        self.assertNotIn("everyone:", acl, "管理 token 对 Everyone 开放")
        self.assertNotIn("\u6bcf\u4e2a\u4eba:", acl, "管理 token 对 Everyone 开放")
        self.assertNotIn("\\users:", acl, "管理 token 对普通 Users 组开放")
        self.assertNotIn("builtin\\users:", acl, "管理 token 对普通 Users 组开放")

    def stop_gracefully(self, process: subprocess.Popen[str]) -> str:
        if process.poll() is None:
            try:
                process.send_signal(getattr(signal_module(), "CTRL_BREAK_EVENT"))
            except (OSError, ValueError):
                pass
        try:
            output, _ = process.communicate(timeout=20)
        except subprocess.TimeoutExpired:
            # Cleanup the whole process tree so a failed assertion cannot leave a service behind.
            subprocess.run(["taskkill", "/PID", str(process.pid), "/T", "/F"], capture_output=True, timeout=10)
            output, _ = process.communicate(timeout=10)
            self.fail("启动器未在 Ctrl+C 后有限时间内停止")
        self.assertTrue(port_is_free(self.port), "启动器退出后监听端口仍被占用")
        return output

    def test_first_start_health_auth_private_acl_and_ctrl_c_cleanup(self) -> None:
        process = self.start_launcher()
        try:
            self.wait_ready(process)
            self.assertEqual(http_status(f"http://{self.address}/healthz"), 200)
            self.assertEqual(http_status(f"http://{self.address}/admin/api/artists"), 401)
            token = self.token_value()
            self.assertEqual(http_status(f"http://{self.address}/admin/api/artists", token), 200)
            self.assert_private_acl(self.dev_dir)
            self.assert_private_acl(self.token_file())
        finally:
            output = self.stop_gracefully(process)
        self.assertTrue(token not in output, "启动器输出包含管理 token")

    def test_repeated_start_preserves_token_and_oauth_configuration(self) -> None:
        self.dev_dir.mkdir(parents=True)
        oauth_file = self.dev_dir / "oauth.json"
        # A structurally valid inert config avoids any Python/pip or Pixiv activity.
        oauth_file.write_text('{"client_id":"offline-test-client","client_secret":"offline-test-secret"}\n', encoding="utf-8")
        process = self.start_launcher(no_login_helper=False)
        try:
            self.wait_ready(process)
            first = self.token_value()
        finally:
            self.stop_gracefully(process)

        # Existing OAuth config must bypass Python discovery and execution entirely.
        process = self.start_launcher("-Python", str(self.dev_dir / "不存在的 python.exe"), no_login_helper=False)
        try:
            self.wait_ready(process)
            second = self.token_value()
            self.assertTrue(first == second, "重复启动更换了管理 token")
            self.assertEqual(oauth_file.read_text(encoding="utf-8"), '{"client_id":"offline-test-client","client_secret":"offline-test-secret"}\n')
        finally:
            output = self.stop_gracefully(process)
        self.assertTrue(first not in output, "启动器输出包含管理 token")

    def test_stop_after_ready_stops_child_and_releases_port(self) -> None:
        process = self.start_launcher("-StopAfterReady")
        try:
            output, _ = process.communicate(timeout=20)
        except subprocess.TimeoutExpired:
            subprocess.run(["taskkill", "/PID", str(process.pid), "/T", "/F"], capture_output=True, timeout=10)
            process.communicate(timeout=10)
            self.fail("-StopAfterReady 未在有限时间内退出")
        self.assertEqual(process.returncode, 0, "-StopAfterReady 应正常退出")
        self.assertTrue(port_is_free(self.port), "-StopAfterReady 后监听端口仍被占用")
        if self.token_file().exists():
            self.assertTrue(self.token_value() not in output, "启动器输出包含管理 token")


    def test_release_directory_without_go_starts_and_stop_after_ready(self) -> None:
        source_binary = ROOT / "bin" / "pixiv-curator.exe"
        if not source_binary.is_file():
            self.skipTest("缺少已有 bin/pixiv-curator.exe，无法构造发布包目录")
        release = Path(self.temp.name) / "发布 包含 空格"
        scripts = release / "scripts"
        scripts.mkdir(parents=True)
        shutil.copy2(LAUNCHER, scripts / "dev.ps1")
        shutil.copy2(source_binary, release / "pixiv-curator.exe")
        # Keep Windows tools and PowerShell available while removing any Go installation.
        path_parts = self.env.get("PATH", "").split(os.pathsep)
        filtered = []
        for entry in path_parts:
            candidate = Path(entry) / "go.exe"
            if candidate.is_file():
                continue
            filtered.append(entry)
        env = dict(self.env, PATH=os.pathsep.join(filtered))
        args = ["-Address", self.address, "-DevDir", str(self.dev_dir), "-NoBrowser",
                "-NoLoginHelper", "-StopAfterReady"]
        result = subprocess.run(
            self.command(*args, launcher=scripts / "dev.ps1"), cwd=release, env=env,
            capture_output=True, text=True, encoding="utf-8", errors="replace", timeout=45,
        )
        self.assertEqual(result.returncode, 0,
                         "无 Go 源码的发布目录启动失败：\n" + self.redact(result.stdout + result.stderr))
        self.assertTrue((self.dev_dir / "curator.db").is_file(), "发布目录启动未使用指定数据目录")
        self.assertTrue(port_is_free(self.port), "发布目录 StopAfterReady 后端口仍被占用")
        if self.token_file().exists():
            self.assertNotIn(self.token_value(), result.stdout + result.stderr, "启动器输出包含管理 token")

    def test_cmd_wrapper_from_another_directory(self) -> None:
        result = subprocess.run(
            ["cmd.exe", "/d", "/c", str(ROOT / "start.cmd"), "-Address", self.address,
             "-DevDir", str(self.dev_dir), "-NoBrowser", "-NoLoginHelper", "-StopAfterReady"],
            cwd=self.temp.name, env=self.env, capture_output=True,
            text=True, encoding="utf-8", errors="replace", timeout=45,
        )
        self.assertEqual(result.returncode, 0, "双击入口从其它工作目录启动失败")
        self.assertTrue((self.dev_dir / "curator.db").is_file(), "双击入口未使用指定数据目录")
        self.assertTrue(port_is_free(self.port), "双击入口诊断结束后端口仍被占用")
        self.assertTrue(self.token_value() not in result.stdout + result.stderr, "双击入口泄漏管理 token")

    def test_occupied_port_fails_without_changing_token_or_stopping_listener(self) -> None:
        self.dev_dir.mkdir(parents=True)
        token_path = self.token_file()
        sentinel = "preexisting-offline-admin-token-value"
        token_path.write_text(sentinel + "\n", encoding="ascii")
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as occupied:
            occupied.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            occupied.bind(("127.0.0.1", self.port))
            occupied.listen(1)
            with socket.create_connection(("127.0.0.1", self.port), timeout=2):
                accepted, _ = occupied.accept()
                accepted.close()
            result = subprocess.run(
                self.command("-Address", self.address, "-DevDir", str(self.dev_dir), "-NoBrowser", "-NoLoginHelper"),
                cwd=ROOT,
                env=self.env,
                capture_output=True,
                text=True,
                encoding="utf-8",
                errors="replace",
                timeout=30,
            )
            self.assertNotEqual(result.returncode, 0, "占用端口时启动器应提前失败")
            self.assertEqual(token_path.read_text(encoding="ascii"), sentinel + "\n", "端口失败时修改了既有 token")
            self.assertEqual(occupied.getsockname(), ("127.0.0.1", self.port), "占用监听器状态异常")
            with socket.create_connection(("127.0.0.1", self.port), timeout=2):
                accepted, _ = occupied.accept()
                accepted.close()


def signal_module():
    # Delayed import keeps the skipped non-Windows run platform-neutral.
    import signal

    return signal


if __name__ == "__main__":
    unittest.main(verbosity=2)
