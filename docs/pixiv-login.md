# Pixiv 账号登录与一键联调

更新：2026-10-10。

## 启动

在装有 Go 1.26+、Python 3.10+（含 venv/pip）和桌面环境的 Linux/macOS 上执行：

```sh
./scripts/dev.sh
```

可以从其他目录调用脚本的绝对路径。脚本自动准备独立 Python 环境、锁定登录依赖、Chromium、匹配的 OAuth 配置和管理 token，构建并启动 Go 服务，检查就绪后打开管理登录页。管理页面与 API 内嵌在同一服务、同一端口，无需另开前端构建服务。

终端只显示管理 token 文件路径，用本机编辑器打开并复制到登录页。文件默认在 `data/dev/admin-token`；不会输出令牌内容。登录管理页后进入「Pixiv 账号」，点击「打开 Pixiv 登录窗口」，在弹出的 **Pixiv 官方页面**输入账号、密码并完成验证码/两步验证。登录成功后自动保存 refresh token，页面只返回保存状态；接着可同步关注。任务仍需人工批准画师，暂停的任务也需显式恢复。

登录窗口出现在**运行 Go 服务的电脑**。远程服务器或无桌面容器请继续使用原来的 refresh token 导入方式。登录总等待时间最多 6 分钟，同一时刻仅允许一次登录，每分钟最多启动 3 次；可以点击取消。服务重启清除登录状态，已成功保存的 refresh token 保留。

首次下载依赖和浏览器需要网络。缺少系统动态库时按 Playwright 的诊断安装所需系统包，脚本不会自动 sudo；不要在 root 用户下关闭浏览器沙箱来规避错误。仅检查本地页面/API 可使用：

```sh
./scripts/dev.sh --no-login-helper
```

此模式不安装登录依赖、不启用浏览器登录。启动脚本的 Ctrl+C 会停止其启动的服务和未完成登录；不会停止占用端口的其他服务。

## 可选配置

```sh
PIXIV_CURATOR_DEV_DIR=/absolute/private/curator-dev \
PIXIV_CURATOR_ADDR=127.0.0.1:5723 \
PIXIV_CURATOR_OPEN_BROWSER=0 \
./scripts/dev.sh
```

默认目录 `data/dev` 被 Git 忽略，保存独立目录数据库、私密凭据、Python 环境与浏览器缓存。已有管理 token 保留；不要在多个进程中共用该数据库。页面或 Go 代码修改后停止并重新运行脚本即可重新构建。

手动启动二进制时，额外配置 `PIXIV_CURATOR_LOGIN_PYTHON`（隔离 Python 的绝对路径）与 `PIXIV_CURATOR_LOGIN_HELPER`（`scripts/pixiv-login.py` 绝对路径），并提供现有 OAuth/refresh token 文件配置。两个登录路径必须同时设置；不设置则保留纯 Go 运行方式。镜像默认不打包 Python/Chromium，也不提供桌面登录窗口。

## 第三方接入

采用 [eggplants/get-pixivpy-token](https://github.com/eggplants/get-pixivpy-token) 的 PyPI 发布版 **gppt 5.1.0**，MIT 许可。使用公开库接口 `gppt.login(headless=False)`；不使用会保存账号密码的 profile/get_token 模式，也不复制其 Python 实现。许可副本和来源见 [third-party.md](third-party.md)。所有 Python 运行依赖版本锁定于 `scripts/requirements-login.txt`，不会改动 Go 依赖。

启动 helper 从所安装 gppt 的客户端常量生成匹配的私密 OAuth 文件；仓库不写入应用密钥常量。存在其他客户端配置时拒绝覆盖，避免混用。登录结果通过受限子进程管道返回 Go，Go 使用现有原子凭据写入接口保存。第三方原始异常、回调 URL 和令牌不进入 HTTP 或服务日志。

适配层关闭 token 请求重定向、禁止登录过程中自动安装浏览器/系统包，并启用 Chromium 沙箱。浏览器会使用临时会话目录，退出/取消后清理。网站 UI、验证码或上游协议变化可能导致登录失败；该功能不绕过验证码，也不代表已完成真实账号验收。

## 接口与验证

全部接口沿用管理 Bearer/会话鉴权；Cookie 写请求需要 Origin 与 CSRF：

| 接口 | 行为 |
| --- | --- |
| `GET /admin/api/account/login` | 返回 enabled、state、脱敏 error_code 和时间 |
| `POST /admin/api/account/login`，JSON `{}` | 启动异步本机登录，202；重复运行409，频率超限429 |
| `POST /admin/api/account/login/cancel` | 请求取消，202；轮询直到 canceled |

状态为 idle/running/succeeded/failed/canceled。succeeded 表示授权已保存，后续采集成功与否请看后台任务；不自动开始采集或修改 Pixiv 关注。

离线检查：

```sh
make check
python3 scripts/test-login-helper.py
sh -n scripts/dev.sh
```

测试覆盖并发登录、取消/超时、保存失败、鉴权/CSRF、错误脱敏和 OAuth 重定向，均不访问 Pixiv。真实账号验收需用户在 Pixiv 窗口完成登录，再验证同步和出图；不要把模拟测试当作实网通过。

## Google 登录提示“浏览器可能不安全”

辅助窗口由 Playwright 控制，Google 会限制自动化浏览器登录，见 [Google 支持说明](https://support.google.com/accounts/answer/7675428)。当前集成不支持在辅助窗口中使用 Google 登录，不通过隐藏自动化标记或关闭浏览器安全功能绕过限制。

可在辅助窗口选择 Pixiv 自身的“通过邮箱登录”并完成验证。如果不确定绑定邮箱，可在日常浏览器通过 Google 登录 [Pixiv 账号门户](https://accounts.pixiv.net/)，确认邮箱或管理登录方式，再回辅助窗口重试。参考 [Pixiv 官方登录说明](https://www.pixiv.help/hc/en-us/articles/235583008-I-ve-lost-my-password)。日常浏览器的登录不会自动传回本项目；不要将 Google 密码填入 Pixiv 密码栏。真实账号验证仍需用户操作。

管理登录页的“记住登录（30 天）”只保存会话 Cookie，不保存管理令牌。未勾选时为浏览器会话 Cookie，服务端最长 12 小时；退出登录或服务重启均使会话失效。
