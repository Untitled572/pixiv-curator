# Pixiv 浏览器登录旧流程（历史存档）

状态：**已从主应用流程移除**。更新：2026-10-15。

旧版本曾通过 Python/gppt helper 启动浏览器 PKCE 流程，支持 Linux 桌面协议回调或手动提交授权 code。该页面、`/admin/api/account/login*` 路由以及登录状态机不再属于当前产品；本文件保留旧流程背景，不能作为当前账号添加步骤或能力保证。

当前请在管理页的“设置 → Pixiv 账号”输入 Refresh Token。服务端会通过 OAuth 刷新验证 Pixiv 用户身份后保存；账号和凭据详情见[运行与接入](running.md)。旧浏览器登录 helper 仍保留在仓库作为历史工具；开发启动脚本仅在 OAuth 应用参数文件缺失时使用锁定 gppt 依赖生成参数文件，已存在配置时不再安装登录依赖，但脚本仍使用 Python 3 标准库。

此前归档的流程以用户自己的浏览器完成 Pixiv 交互，涉及账号登录、reCAPTCHA/两步验证和 Pixiv 网络条件；从未保证真实认证必然成功。离线或 fake transport 测试不代表真实账号登录、Pixiv API 连通或图像 URL 可达。主流程不会打开浏览器、收集 Pixiv 密码或提交授权 code。
