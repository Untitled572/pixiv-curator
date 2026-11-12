# Pixiv Curator

用 Go 构建的本地 Pixiv 目录和选图服务，可向 PicHub-Aggregator 提供图片 URL。Curator 保存画师、作品、分页元数据和 URL；图片由 Curator 按需流式转发以处理防盗链，不缓存原图；原图归档、收藏与后续分发由调用方（例如 PicHub）负责。

## 当前状态

核心目录、任务、推荐、管理鉴权、选图接口和管理页面已经实现。多 Pixiv 账号、账号来源筛选、画师标签修正和新的出图方式已完成本轮实现。全仓 `go vet ./...`、`go test -race ./...`，Linux/Windows amd64、arm64 构建和隔离浏览器桌面/手机验证通过；此前离线验收与本轮实网检查分开记录；Windows 原生完整关注快照、单画师采集、推荐和管理预览已实测通过，见[本轮修复记录](docs/runtime-fixes-2026-11-12.md)。PicHub 联调仍待验证。请以[开发计划](docs/development-plan.md)中的本轮状态为准。

添加账号时验证 Refresh Token 对应的 Pixiv 身份并安排首次关注同步；同步发现的关注画师自动批准、加入采集并入队，`blocked` 和 `rejected` 状态保留。推荐画师仍需预览后由用户明确加入。默认每 6 小时安排关注与采集增量扫描，周期从进程启动后计时。旧单账号迁移先以 UID 0 导入，首次成功同步绑定真实身份；绑定前需修复该账号，不能再添加其他账号。取关或删除账号保留已采集历史，屏蔽始终优先。

## 启动

```sh
./scripts/dev.sh
```

本机默认地址为 <http://127.0.0.1:5722>，开发数据默认保存在 `data/dev`。需要 Go 1.26+。启动脚本生成管理令牌；缺少 OAuth 应用配置时，脚本使用锁定版本的 gppt 准备配置。OAuth 配置已存在时，日常启动不再安装登录依赖，但脚本仍使用 Python 3 标准库。可设置 `PIXIV_CURATOR_OPEN_BROWSER=0` 关闭自动打开页面。生产部署、首次配置和凭据迁移见[运行与接入](docs/running.md)。

Windows 可双击根目录的 `start.cmd`，或在 PowerShell 中运行：

```powershell
.\scripts\dev.ps1
# 不自动打开浏览器：
.\scripts\dev.ps1 -NoBrowser
```

Windows 脚本支持 PowerShell 5.1/7：源码目录自动构建，下载的 Windows 发行包直接运行自带 EXE（不需要 Go）；两者均准备管理令牌；首次缺少 OAuth 配置时才用 Python 3 和锁定依赖生成配置，之后日常启动不需要 Python/pip。数据默认位于 `data/dev`，令牌不回显，文件 ACL 限当前用户访问；按 Ctrl+C 或关闭启动窗口时清理其子进程。自定义目录、端口和离线诊断见[运行与接入](docs/running.md#windows-原生启动)。

主应用使用 Pixiv Refresh Token，不提供浏览器 PKCE 登录流程。`docs/pixiv-login.md` 保留此前 Python 浏览器登录方式的历史说明，不属于当前主流程。

登录管理页后有四个页面：画师图库、发现画师、统计与接口、设置。图库按页加载文字信息，可按账号和标签筛选；总数由服务端统计，代表作只在打开详情后加载。统计与接口页显示目录数量，并提供供图地址生成、复制和 JSON 调用测试；设置包含 Pixiv 账号、服务端 API 代理和选图偏好。当前没有独立任务页面，后台任务通过服务结构化日志记录状态和错误码。

## 图片 API

默认 JSON 返回可直接访问的本服务图片地址，保留 Pixiv 原图地址字段 original_url：

~~~text
GET /random                                  # JSON：url 为固定图片转发地址
GET /random?format=image                     # 直接返回图片
GET /random?format=redirect                  # 302 到固定图片转发地址
GET /random?delivery=original                # JSON：url 为 Pixiv 原图地址
GET /i/{illust_id}/{page_index}?format=image  # 固定作品页图片，不重新随机
~~~

Curator 使用服务端网络设置并携带 Pixiv Referer 获取图片；浏览器或 PicHub 访问默认 url 无需设置 Referer。原图字节限时、限大小流式转发，不落盘；仅管理缩略图使用 preview-cache（256 MiB / 单张 8 MiB / 7 天）。JSON 和 302 本身不会拉取图片字节。显式 delivery=original 的直接原图模式仍需消费端网络和 Pixiv Referer。

PIXIV_CURATOR_PUBLIC_BASE_URL 用于生成 JSON 图片地址和管理 Origin 校验，必须配置为消费端能访问的服务地址；默认 127.0.0.1 只适合本机使用。

账号、标签、尺寸、分级和 AI 筛选可以组合。默认只允许 safe / non_ai；未知值保持独立分类。设置中显式允许 unknown 后，才可用 r18=unknown / ai_type=unknown 筛选；参数不能放宽全局排除。

发现页可按 R18、AI 的“是、否、未知、不指定”组合筛选；类别依据本轮代表作样本，不能代表画师全部作品。卡片使用信息与缩略图上下布局，没有单独大封面空区。

/healthz 表示进程存活；/readyz 检查本地数据库与 Worker，不代表 Pixiv 或 CDN 可达。

## 配置与安全

程序通过环境变量读取配置，不自动读取 `.env`。完整列表见 [.env.example](.env.example) 与[详细设计](docs/detailed-design.md)。管理 API 的读写请求都需要 Bearer token 或管理会话；Cookie 写请求还校验 Origin 和 CSRF。凭据只接受写入，管理响应不回显 Refresh Token。

Pixiv Refresh Token、OAuth 配置、管理令牌和带代理凭据的网络设置必须放在受限私密目录；不得提交真实凭据、数据库或运行日志。旧单账号配置可迁移为 `legacy` 账号，原 SQLite 数据库中的偏好和采集历史继续保留。备份与恢复见[运行与接入](docs/running.md)。

## 开发检查

```sh
go test ./...
go vet ./...
```

任务、数据库、令牌刷新或并发变更还应按 [AGENTS.md](AGENTS.md) 运行 race 检查。没有真实网络验收时，不能把 mock 或编译通过称为 Pixiv/PicHub 联调通过。

## 文档

- [详细设计](docs/detailed-design.md)：状态、数据、同步、标签、供图和接口契约。
- [开发计划](docs/development-plan.md)：工作状态、验收记录与剩余事项。
- [架构概要](docs/architecture.md)：模块与数据流。
- [运行与 PicHub 接入](docs/running.md)：配置、账号迁移、网络边界、备份与部署。
- [历史 Pixiv 浏览器登录流程](docs/pixiv-login.md)：此前流程的存档说明。
- [开发规范](AGENTS.md)：实现、安全、验证和发布边界。
