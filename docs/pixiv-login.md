# Pixiv 账号登录与一键联调

更新：2026-10-15。

## Linux 桌面：自动回调

本机桌面运行 `./scripts/dev.sh` 后，会为当前用户注册 **Pixiv Curator Login** 接收器。管理页打开登录标签，完成登录后允许浏览器打开 **Pixiv Curator Login**，然后回管理页等待授权保存，无需 F12。

仅适用于浏览器与服务在同一台 Linux 桌面、同一用户下运行。已有其他应用的协议关联不会被覆盖。设置 `PIXIV_CURATOR_LOCAL_CALLBACK=0` 可禁用自动注册。无 DISPLAY/WAYLAND 的服务器不注册桌面协议。接收器使用 0700 目录及 0600 Unix socket，不把授权码写入普通文件。

注册文件为 `~/.local/share/applications/pixiv-curator-login.desktop`（遵循 XDG_DATA_HOME）。卸载时删除该文件并在用户 mimeapps.list 移除相应关联。

## Docker 与跨设备部署

服务器不需要 GUI 或浏览器。纯 Go Docker 镜像不包含 Python/gppt，也不提供桌面回调；使用管理页已有的 Refresh Token 导入功能即可。可在有桌面的电脑上完成本机授权，将其私密 `data/dev/pixiv-refresh-token` 文件内容粘贴到远端管理页的 Refresh Token 框保存。请使用可信 HTTPS 或 SSH 隧道访问远端管理页，不通过聊天传递凭据。本机与远端不要同时运行采集任务并共用轮换中的 token。

如果服务器另行安装了 Python helper，可在客户端浏览器打开服务器生成的 PKCE 链接并手动回填 code。浏览器和服务器不在同一设备时，应禁用 LOCAL_CALLBACK；Linux 接收器不能自动把本机 code 交给远端服务。跨设备自动回传尚未实现。

## 普通浏览器手动回填（备用）

启动服务并登录管理页面后，打开「Pixiv 账号」页，点击「打开 Pixiv 登录」。页面会在点击动作中预先打开一个新标签，待服务返回授权地址后导航到 Pixiv。若浏览器拦截或预留标签无法导航，可使用页面显示的“手动打开 Pixiv 登录链接”。

使用普通浏览器完成 Pixiv 登录。登录前建议按 F12 打开开发者工具，在 Network 面板勾选 **Preserve log**，并筛选 `callback` 或 `pixiv`。登录完成后，在请求记录中找到 `pixiv://account/login?code=...` 回调，从请求 URL 复制 `code` 参数的值，或复制完整回调 URL，粘贴到管理页的授权输入框并提交。授权 code 有效期很短，取得后应立即提交；失效需重新开始登录。输入框采用密码类型；提交时页面会立即清空输入内容。服务端用 gppt 提供的 `oauth.generate_pkce`、`oauth.extract_code` 与 `token.exchange` 处理授权 code 并保存 refresh token。这里通过用户自己的浏览器完成页面交互，不使用 Playwright 或其他浏览器自动化。

验证码、两步验证及其他 Pixiv 风控是否通过由 Pixiv 决定，本流程不保证每次验证都能成功。若无法取得回调 code，可重试，或使用下方 Refresh Token 手动保存方式。Google 旧自动化登录方式已不再支持；请在普通浏览器里选择 Pixiv 提供的登录方式完成授权。

## 配置与限制

`./scripts/dev.sh` 准备 Python OAuth 依赖与私密配置并构建启动 Go 服务；需要 Python 3.10+（含 venv/pip），但登录无需 Chromium 或服务端桌面环境。默认开发数据目录为 `data/dev`，其中保存独立数据库和私密凭据。脚本的 `--no-login-helper` 参数可用于离线查看页面/API，此时不启用登录辅助能力。首次启动和管理 token 说明见 [README](../README.md)。

登录在服务端最多等待 6 分钟，同一时刻仅允许一次登录，每分钟最多启动 3 次；可点击取消。服务重启会清除正在等待的登录状态，已成功保存的 refresh token 保留。远程部署时，登录链接会在打开管理页的浏览器中访问；无需在运行 Go 服务的主机上打开窗口。

第三方 OAuth 实现复用 [eggplants/get-pixivpy-token](https://github.com/eggplants/get-pixivpy-token) 发布版 **gppt 5.1.0** 的 `oauth.generate_pkce`、`oauth.extract_code` 与 `token.exchange`，许可与来源见 [third-party.md](third-party.md)。这些是 `oauth_login` 使用的同一组函数；Go 端不实现另一套 token 交换协议。登录链接从后端状态接口取得；前端只允许导航到 `https://app-api.pixiv.net/web/v1/login` 或兼容的 `https://accounts.pixiv.net/login` 地址，不接受其他域名。授权 code 只通过鉴权后的管理 API 提交，不写入 URL、日志或浏览器存储。

## 接口

全部接口沿用管理 Bearer/会话鉴权；Cookie 写请求需要 Origin 与 CSRF：

| 接口 | 行为 |
| --- | --- |
| `GET /admin/api/account/login` | 返回 enabled、state、脱敏 error_code 和时间；仅在 running 且授权链接就绪后包含 `login_url` |
| `POST /admin/api/account/login`，JSON `{}` | 启动授权流程，返回 202 |
| `POST /admin/api/account/login/code`，JSON `{"code":"…"}` | 提交 code 或完整 `pixiv://account/login?code=...` 回调 URL，返回 202；code 仅允许提交一次 |
| `POST /admin/api/account/login/cancel` | 取消当前流程，返回 202 |

状态包括 idle/running/succeeded/failed/canceled。succeeded 表示授权已保存；不会自动开始采集或修改 Pixiv 关注。后续采集任务仍需单独触发与管理。

## 验证说明

离线测试不访问 Pixiv，不能证明真实账号登录或验证码流程成功。真实联调应由用户在普通浏览器完成 Pixiv 登录，并确认状态变为成功；不要将 mock、页面加载或 API 接受请求描述为真实认证通过。

管理登录页的“记住登录（30 天）”只保存会话 Cookie，不保存管理令牌。未勾选时为浏览器会话 Cookie，服务端最长 12 小时；退出登录或服务重启均使会话失效。
