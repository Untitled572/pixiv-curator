# 运行、账号与 PicHub 接入

本文描述当前运行契约；本轮 Windows/Linux 检查和真实 Pixiv 验证见[开发计划](development-plan.md)末尾。历史隔离 UI 验收不能代替本轮实网结果；PicHub 下载、显示和收藏仍需单独联调。

## 本机启动

```sh
./scripts/dev.sh
```

默认监听 `127.0.0.1:5722`，数据存于 `data/dev`，可用 `PIXIV_CURATOR_DEV_DIR` 指定目录。脚本创建私密管理 token，并启动 API、后台 Worker 和 Go 模板管理页。当前主应用只使用 Refresh Token 导入，不提供浏览器 PKCE 登录。

开发脚本会准备 OAuth 应用参数文件。缺少该文件时才准备锁定的 gppt 依赖并生成文件；文件已经存在时正常启动不再安装登录依赖，但脚本仍使用 Python 3 标准库。避免手工覆盖已有 OAuth 参数。无须自动打开浏览器时设置 `PIXIV_CURATOR_OPEN_BROWSER=0`。旧 Python 浏览器授权流程见[历史登录说明](pixiv-login.md)，不属于当前运行步骤。

生产部署可直接运行构建的 Go 二进制。至少设置：

```sh
export PIXIV_CURATOR_ADDR=127.0.0.1:5722
export PIXIV_CURATOR_DATABASE_PATH=/srv/pixiv-curator/curator.db
export PIXIV_CURATOR_ADMIN_TOKEN_FILE=/srv/pixiv-curator/private/admin-token
export PIXIV_CURATOR_OAUTH_CONFIG_FILE=/srv/pixiv-curator/private/oauth.json
export PIXIV_CURATOR_PUBLIC_BASE_URL=http://127.0.0.1:5722
./pixiv-curator
```

管理 token 与 OAuth 文件应处于私密目录，POSIX 目录权限 0700、文件权限 0600。管理 token 文件不配置或为空时管理操作拒绝。`PUBLIC_BASE_URL` 只用于管理 Origin 校验，不用于拼装 `/random` 图片 URL。

## Windows 原生启动

Windows 需要 Go 1.26+，原生启动不运行 Linux 的 dev.sh。先在私密位置准备管理 token 文件和 OAuth 应用配置（可复用已有安装的 OAuth 配置，不复制到 Git 跟踪目录），再在项目根目录设置：

```powershell
$env:PIXIV_CURATOR_ADDR = "127.0.0.1:5722"
$env:PIXIV_CURATOR_DATABASE_PATH = Join-Path $PWD "data/curator.db"
$env:PIXIV_CURATOR_ADMIN_TOKEN_FILE = "D:\private\pixiv-curator\admin-token"
$env:PIXIV_CURATOR_OAUTH_CONFIG_FILE = "D:\private\pixiv-curator\oauth.json"
$env:PIXIV_CURATOR_PUBLIC_BASE_URL = "http://127.0.0.1:5722"
go run ./cmd/api
```

以上私密文件路径为示例，请替换为实际位置。管理 token 应为随机非空单行值，Windows 私密目录通过 NTFS ACL 仅授权当前用户；不要把真实值写进启动脚本。数据库支持含中文、空格、#、% 的本机路径。启动后进入管理页，使用 Refresh Token 添加测试或正式账号；只有添加账号才访问 Pixiv 验证身份。

## 添加、迁移和管理 Pixiv 账号

在管理页登录后进入“设置 → Pixiv 账号”，输入显示名称和 Refresh Token 并保存。服务端会尝试 OAuth 刷新并验证返回的 Pixiv 用户 ID；只有身份验证成功才新增账号并入队首次关注同步。账号 ID 和 Refresh Token 绑定，更新凭据必须仍属于同一 Pixiv 身份。相同 Pixiv 用户 ID 不能重复添加。Token 只写入独立私密文件，响应和账号列表不回显 token 或文件路径。

首次同步成功后，账号关注关系写入 `account_artists`，发现的关注画师自动批准、启用采集并入队，`blocked` 和 `rejected` 状态保留。同一画师由多个账号关注时关系去重，作品目录共用。服务在运行期间每 6 小时为活动账号安排关注同步，并为启用采集的画师安排增量扫描。周期从本进程启动时开始，重启后重新计时；下次触发时间没有持久化。账号加入后首次关注同步独立入队，不必等 6 小时。缺省手动同步和发现使用首个已配置账号，不会因限速自动换号。

取消关注或删除账号不会删除已采集画师、作品、图片 URL、偏好或账号来源历史。删除账号会从活动账号列表和筛选项移除，并删除对应 token 文件；由于账号已软删除，不能再用该账号 ID 过滤。默认供图仍可使用其他条件合格的历史作品。画师屏蔽始终优先：被屏蔽画师不能供图，未完成任务会停止或取消。

旧版单账号安装可继续配置 `PIXIV_CURATOR_PIXIV_TOKEN_FILE`。启动时会将其导入为 UID 0 的 `legacy` 账号，并把旧的关注标记转换为账号来源关系。首次成功同步后才绑定真实 Pixiv UID；在此之前不能添加第二账号，需先修复 legacy 凭据和身份。迁移复用原 SQLite 数据库，旧偏好、画师、作品和图片分页都保留；迁移前仍应备份数据库和凭据。多账号凭据位于数据库旁的 `private/accounts` 目录，不要只备份数据库。

## 管理页面与任务观察

管理页面包含三个标签页：

- **画师图库**：按标签和 Pixiv 账号来源筛选已批准/已屏蔽画师，浏览作品；可编辑本地画师标签。自动标签由近期 100 件作品的标签频次取前 8。保存 `[]` 会明确清空覆盖；删除覆盖后恢复自动标签。
- **发现画师**：查看有限代表作预览与推荐理由。确认后才批准并安排采集；推荐不会自动批准。
- **设置**：管理 Pixiv 账号、服务端 API 网络设置和选图偏好。

当前没有独立任务 UI。任务开始、结束、状态、稳定错误码和重试时间通过服务结构化日志观察。任务/游标保存在数据库中，重启后由 Worker 恢复；6 小时周期计时器本身不持久化。`/healthz` 仅表示 HTTP 进程存活；`/readyz` 检查本地数据库和 Worker，不检查 Pixiv 连通性。

## 选图 API

默认 JSON 返回数据库中经过校验的 Pixiv 原图 URL：

```sh
curl 'http://127.0.0.1:5722/random?collection=preferred'
```

可显式请求 JSON，或选择 302 跳转：

```sh
curl -i 'http://127.0.0.1:5722/random?format=json&account_id=legacy&included_tags=猫|风景'
curl -i 'http://127.0.0.1:5722/random?format=redirect&account_id=legacy'
```

重复 `included_tags` 参数表示多组必须同时匹配；同一参数值用 `|` 表示任选。可组合 `excluded_tags`、`user_id`、`account_id`、方向、最小尺寸、`r18`、`ai_type` 与策略。每次改变条件时，仍执行硬过滤。默认只允许 `safe` 与 `non_ai`；分类未知的作品保持未知并排除。作品 dislike、画师屏蔽、不可见作品不会通过小库降级放行。近期去重不足时只允许重复一张合格旧图并标记 `repeated`。

`format=redirect` 返回 HTTP 302，公开供图不下载、代理或缓存图片；仅管理预览允许鉴权后的限时、限大小流式转发，不落盘。消费方须自行访问返回的 Pixiv 图片 URL；浏览器、PicHub 的代理和 DNS 设置独立于 Curator 服务端。图片端点 `/i/{illust_id}/{page_index}` 固定相同作品页身份，可返回 URL 或按 `format=redirect` 跳转。

PicHub 可设置 JSON 响应，图片字段路径为 `url`。按需把 `account_id` 和标签条件配置在图源 URL 查询参数。不要在 PicHub Headers 中放 Curator 管理令牌或 Pixiv 凭据。由于公开供图不代理原图，部署前要从 PicHub 所在网络确认图片 URL 可达，并配置 `Referer: https://www.pixiv.net/`。管理预览的代理与请求头不会自动传给 PicHub。

供图 API 当前没有单独服务鉴权，应仅部署在受控本机或内网。不能因为管理页面有登录就将公开 `/random` 暴露到公网。

## 网络设置边界

设置页中的服务端代理模式影响 Curator 后续发出的 Pixiv OAuth、App API 和管理预览请求；它不控制 PicHub 下载原图时使用的网络。浏览器读取已鉴权的 Curator 管理预览端点，服务端带 Pixiv Referer 并使用服务端网络设置拉取该固定页；PicHub 按其运行环境访问返回的原图 URL，并设置 `Referer: https://www.pixiv.net/`。保存代理只影响新建请求，不切换正在运行的请求。

未保存网络配置时，服务端默认使用 `http://127.0.0.1:7890` HTTP 代理；已保存的配置保持不变。设置页明文显示代理地址，支持继承环境变量、直连或配置 HTTP/HTTPS 代理。环境变量包括 `HTTP_PROXY`、`HTTPS_PROXY`、`NO_PROXY` 及小写对应项；不自动读取桌面 PAC，也不支持 SOCKS。自定义代理凭据只写入私密配置，管理 GET 不回显密码。Docker 中的 `127.0.0.1` 指容器自身，代理必须从容器网络可达。

网络设置文件默认是数据库父目录下的 `private/network.json`，可由 `PIXIV_CURATOR_NETWORK_CONFIG_FILE` 指定。更换部署位置时要一并迁移此文件。

## 数据、迁移与备份

迁移按版本 001–007 前进：006 增加账号与账号/画师关联；007 增加手动画师标签覆盖。程序在启动时事务应用迁移并拒绝未知 schema 版本。更新前备份数据库和私密凭据；旧的 SQLite 偏好与作品数据不会因增加账号关系而删除。

停机后可完整备份数据库；运行中请使用 SQLite `.backup` 或程序的 `Store.Backup` 一致性快照，不要只复制活跃 WAL 数据库的主 `.db` 文件。分别保存 OAuth 配置、管理 token、多账号 Refresh Token 目录和网络设置文件。恢复后检查 `/readyz` 并重新登录管理页；管理会话不从备份恢复。不要把多实例指向同一 SQLite 文件。

## 尚未验收

离线/mock 测试不代表 Pixiv 实网认证、真实账号关注同步、CDN 原图访问或 PicHub 下载/显示/收藏已经通过。本轮全仓 race 测试、四个平台构建、Docker、浏览器 UI 与真实网络联调的状态以[开发计划](development-plan.md)为准。
