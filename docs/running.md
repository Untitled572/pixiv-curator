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

管理 token 与 OAuth 文件应处于私密目录，POSIX 目录权限 0700、文件权限 0600。管理 token 文件不配置或为空时管理操作拒绝。PIXIV_CURATOR_PUBLIC_BASE_URL 用于管理 Origin 校验和生成 JSON 图片地址；应设置为浏览器/PicHub 均能访问的服务地址。不要使用消费端不可达的 127.0.0.1。

## Windows 原生启动

双击仓库根目录 `start.cmd`，或在 PowerShell 中运行：

```powershell
.\scripts\dev.ps1
```

需要 Go 1.26+ 和 Windows PowerShell 5.1 / PowerShell 7，凭据目录需支持 NTFS ACL。默认数据目录 `data/dev`，监听 `127.0.0.1:5722`，脚本构建 `bin/pixiv-curator.exe`、生成随机管理令牌、等待本机 `/readyz` 成功后打开登录页。登录令牌保存在窗口显示的文件路径，脚本不会输出令牌内容。`start.cmd` 只为当前子进程设置执行策略，不更改系统执行策略。

首次缺少 OAuth 应用参数时需要 Python 3；脚本在 `venv-windows` 独立环境中安装现有锁定依赖并生成 `oauth.json`，不下载浏览器、不调用 Pixiv 账号认证。配置已存在时保留原文件，日常启动不调用 Python 或 pip。通过设置页输入 Refresh Token 后才验证账号身份。

```powershell
# 不自动打开浏览器
.\scripts\dev.ps1 -NoBrowser

# 自定义监听与私密数据目录（相对目录以仓库为基准）
.\scripts\dev.ps1 -Address 127.0.0.1:8080 -DevDir 'D:\private\pixiv-curator'

# 复用已有 OAuth 应用配置，或指定首次准备时的 Python
.\scripts\dev.ps1 -OAuthConfigFile 'D:\private\pixiv-curator\oauth.json'
.\scripts\dev.ps1 -Python 'C:\Python313\python.exe'

# 离线启动诊断：不准备 OAuth，服务就绪后立即结束
.\scripts\dev.ps1 -NoLoginHelper -NoBrowser -StopAfterReady
```

`PIXIV_CURATOR_DEV_DIR`、`PIXIV_CURATOR_ADDR`、`PIXIV_CURATOR_OAUTH_CONFIG_FILE`、`PIXIV_CURATOR_PUBLIC_BASE_URL` 及 `PIXIV_CURATOR_OPEN_BROWSER=0` 也可配置，显式参数优先。脚本统一使用数据目录内的 `admin-token`、`curator.db`、可选旧账号 `pixiv-refresh-token`；高级部署可直接运行二进制并按前面的环境变量方式配置。

端口占用时，脚本在修改私密文件和构建前退出，不终止已有监听者。已有管理令牌与 OAuth 配置不覆写；普通凭据文件不接受符号链接或联接，私密目录及凭据通过 DACL 限制访问。路径支持中文和空格。

按 Ctrl+C/Ctrl+Break 或关闭启动窗口会清理脚本创建的进程树，不影响其它服务。Windows 结束子进程与 POSIX SIGTERM 不同，未完成任务由持久租约在重启后恢复。后台结构化日志保留在启动窗口，不另写原图或第二套图片缓存。

PowerShell 7 / Windows PowerShell 5.1 原生离线测试：

```powershell
python scripts/test-windows-launcher.py
$env:PIXIV_CURATOR_TEST_POWERSHELL = "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe"
python scripts/test-windows-launcher.py
```

## 添加、迁移和管理 Pixiv 账号

在管理页登录后进入“设置 → Pixiv 账号”，输入显示名称和 Refresh Token 并保存。服务端会尝试 OAuth 刷新并验证返回的 Pixiv 用户 ID；只有身份验证成功才新增账号并入队首次关注同步。新账号的推荐批准后远端关注选项默认关闭，可在账号设置中显式开启；已有账号也默认关闭。账号 ID 和 Refresh Token 绑定，更新凭据必须仍属于同一 Pixiv 身份。相同 Pixiv 用户 ID 不能重复添加。Token 只写入独立私密文件，响应和账号列表不回显 token 或文件路径。

首次同步成功后，账号关注关系写入 `account_artists`，发现的关注画师自动批准、启用采集并入队，`blocked` 和 `rejected` 状态保留。一次同步任务先完整读取公开关注，再读取私密关注；每阶段的游标持久化，只有两阶段都完整完成才把未见关系标记为取关。`PATCH /admin/api/accounts/{id}` 设置默认账号或 `follow_on_approve`；只有推荐来源候选从待审状态经用户明确批准才会排队远端关注，且自动关注只使用 public 可见性。该选项默认关闭，可由用户显式开启。手动 UID、GET 与预览都不触发远端写入。显式 `POST /admin/api/accounts/{id}/copy-following` 可将来源账号最近一次成功同步的公开和私密关注复制到目标账号，并保留各自可见性；重复执行会跳过目标账号已有的同可见性关系，不会取关。关闭选项会取消尚未发起的批准后关注任务；发请求前还会复查选项与候选资格，固定关注端点出站间隔至少两秒。同一画师由多个账号关注时按账号与画师去重，同时保存可见性，作品目录共用。服务在运行期间每 6 小时为活动账号安排关注同步，并为启用采集的画师安排增量扫描。周期从本进程启动时开始，重启后重新计时；下次触发时间没有持久化。账号加入后首次关注同步独立入队，不必等 6 小时。缺省手动同步和发现使用默认账号，不会因限速自动换号。

取消关注或删除账号不会删除已采集画师、作品、图片 URL、偏好或账号来源历史。删除账号会从活动账号列表和筛选项移除，并删除对应 token 文件；由于账号已软删除，不能再用该账号 ID 过滤。默认供图仍可使用其他条件合格的历史作品。画师屏蔽始终优先：被屏蔽画师不能供图，未完成任务会停止或取消。

旧版单账号安装可继续配置 `PIXIV_CURATOR_PIXIV_TOKEN_FILE`。启动时会将其导入为 UID 0 的 `legacy` 账号，并把旧的关注标记转换为账号来源关系。首次成功同步后才绑定真实 Pixiv UID；在此之前不能添加第二账号，需先修复 legacy 凭据和身份。迁移复用原 SQLite 数据库，旧偏好、画师、作品和图片分页都保留；迁移前仍应备份数据库和凭据。多账号凭据位于数据库旁的 `private/accounts` 目录，不要只备份数据库。

## 管理页面与任务观察

管理页面包含五个标签页：

- **画师图库**：按标签和 Pixiv 账号来源筛选已批准/已屏蔽画师。文字列表每页 24 / 48 / 96 位，可前后翻页或跳转；点击画师名称/详情才加载作品；可编辑本地画师标签。自动标签由近期 100 件作品的标签频次取前 8。保存 `[]` 会明确清空覆盖；删除覆盖后恢复自动标签。
- **发现画师**：查看有限代表作预览与推荐理由。推荐任务运行时查询进度，完成并预载代表作后统一展示最多 30 位候选；最多 4 路并发加载预览，候选卡片不会随状态轮询反复重建。确认后才批准并安排采集；推荐不会自动批准。点击代表作打开站内大图，可翻页、查看尺寸和标签，并打开 Pixiv 作品或固定图片链接。
- **分发历史**：最近 30 天最多 10000 条选图记录，含接口预取和 JSON 调用；它不表示图片已成功下载。旧库迁移只回填已有的最近 20 条记录。
- **统计与接口**：查看本地作品/图片分页总数、已索引及不可用数量等统计，生成带筛选的供图 URL、复制地址和测试 JSON 结果。测试固定请求 JSON；302 或图片响应可在新标签页打开。
- **设置**：管理 Pixiv 账号、服务端 API 网络设置和选图偏好。

当前没有独立任务 UI。任务开始、结束、状态、稳定错误码和重试时间通过服务结构化日志观察。任务/游标保存在数据库中，重启后由 Worker 恢复；6 小时周期计时器本身不持久化。`/healthz` 仅表示 HTTP 进程存活；`/readyz` 检查本地数据库和 Worker，不检查 Pixiv 连通性。

## 选图 API

默认返回 JSON，url 为本服务图片转发地址，original_url 为 Pixiv 原图。调用方使用 url 即可避免直接请求 Pixiv 的 Referer 403：

~~~sh
curl 'http://127.0.0.1:5722/random?format=json'
curl 'http://127.0.0.1:5722/random?format=image'
curl -i 'http://127.0.0.1:5722/random?format=redirect'
curl 'http://127.0.0.1:5722/random?format=json&account_id=legacy&included_tags=猫|风景'
# 原图地址兼容模式，直接获取时仍需 Pixiv Referer：
curl 'http://127.0.0.1:5722/random?delivery=original'
~~~

固定图片地址为 /i/{illust_id}/{page_index}?format=image；页码从 0 开始，每次解析同一页并重新检查资格。JSON/302 不下载图片字节；图片模式由 Curator 使用服务端网络、带 Referer 流式获取，20 秒 / 64 MiB，不缓存原图。管理预览仍鉴权，只有缩略图允许有界缓存（preview-cache，256 MiB / 单张 8 MiB / 7 天）。

included_tags 可重复表示多组 AND，同组 | 为 OR。账号、画师、标签、方向、尺寸、R18、AI、策略可组合。默认 safe / non_ai；全局偏好显式勾选相应未知类别后，才可用 r18=unknown 或 ai_type=unknown 进一步筛选。`exclude_manga` 只排除 Pixiv `kind=manga`，普通多页作品仍可入选；未知次页尺寸按 0 处理，不参与方向过滤。PicHub 下载路径用 `DecodeConfig` 读取真实图片尺寸。r18=2、ai_type=any 仅包含已知类别；未知不会当作全年龄或非 AI。dislike、屏蔽、不可见及其它硬排除始终优先。

PicHub 配置 JSON 响应、字段路径 url，源 URL 使用 /random?format=json。默认转发模式只要求 PicHub 能访问 Curator，Curator 能访问 Pixiv；无需在 PicHub 设置 Pixiv Referer，更不要传管理或 Pixiv 凭据。PUBLIC_BASE_URL 必须设置为 PicHub 可达的 Curator origin；若部署在另一主机，127.0.0.1 将指向另一主机自身。显式 delivery=original 才需要 PicHub 自行访问 Pixiv 并设置 Referer。

发现页的 R18/AI 分类由当前代表作样本推断，可组合选择不指定、是、否或未知；未知或缺失样本不推断为否。每张候选卡片内展示缩略图，不保留大封面占位。设置中保存的供图偏好不改变推荐筛选。

## 网络设置边界

设置页中的服务端代理模式影响 Curator 的 OAuth、App API、管理预览及公开图片转发。消费者访问默认 url 时使用 Curator 的图片出口；只有选择 delivery=original 时才使用消费者自己的图片出口。保存代理只影响新建请求，不切换正在运行的请求。

未保存网络配置时，服务端默认使用 `http://127.0.0.1:7890` HTTP 代理；已保存的配置保持不变。设置页明文显示代理地址，支持继承环境变量、直连或配置 HTTP/HTTPS 代理。环境变量包括 `HTTP_PROXY`、`HTTPS_PROXY`、`NO_PROXY` 及小写对应项；不自动读取桌面 PAC，也不支持 SOCKS。自定义代理凭据只写入私密配置，管理 GET 不回显密码。Docker 中的 `127.0.0.1` 指容器自身，代理必须从容器网络可达。

网络设置文件默认是数据库父目录下的 `private/network.json`，可由 `PIXIV_CURATOR_NETWORK_CONFIG_FILE` 指定。更换部署位置时要一并迁移此文件。

## 推荐排序

v0.3.0 每轮目标为17位逻辑回归算法推荐、10位独立历史榜单随机和3位随机ID探测，最多30位、80次上游请求。算法仅使用标签和官方来源特征，依据明确的画师选择训练；探索不依赖本地画像，随机ID探测不能保证严格全站均匀，来源不足时按实际合格数据补位。模型、来源计划和混排顺序持久保存，展示指数不是经校准的喜欢概率。数据和接口约定见[详细设计](detailed-design.md)。

## 数据、迁移与备份

迁移按版本 001–011 前进：006 增加账号与账号/画师关联；007 增加手动画师标签覆盖；008 增加默认账号及默认关闭的 `follow_on_approve`；009 增加分发历史；010 纠正次页未知尺寸；011 为账号关注关系增加可见性。程序在启动时事务应用迁移并拒绝未知 schema 版本。更新前备份数据库和私密凭据；旧的 SQLite 偏好与作品数据不会因增加账号关系而删除。

停机后可完整备份数据库；运行中请使用 SQLite `.backup` 或程序的 `Store.Backup` 一致性快照，不要只复制活跃 WAL 数据库的主 `.db` 文件。分别保存 OAuth 配置、管理 token、多账号 Refresh Token 目录和网络设置文件。恢复后检查 `/readyz` 并重新登录管理页；管理会话不从备份恢复。不要把多实例指向同一 SQLite 文件。

## 尚未验收

离线/mock 测试不代表 Pixiv 实网认证、私密关注同步/复制、CDN 原图访问或 PicHub 下载/显示/收藏已经通过。本轮全仓 race 测试、四个平台构建、Docker、浏览器 UI 与真实网络联调的状态以[开发计划](development-plan.md)为准。

管理缩略图缓存可在服务停止后删除数据库同级 preview-cache，下一次打开详情会重新生成；不影响目录和原图 URL。该目录只供管理预览，不应通过静态网站公开。

前端离线行为回归脚本为 scripts/test-admin-ui.cjs，本轮验证使用 Playwright 1.62.1 与本机 Chrome，运行时需要可用的 Playwright 与 Chromium；可通过 PIXIV_CURATOR_PLAYWRIGHT_MODULE 指定已有模块、PIXIV_CURATOR_BROWSER_EXECUTABLE 指定浏览器。脚本使用本机模拟 API，阻止外部请求，不导入真实账号。


Windows 发行包解压后也提供 start.cmd / scripts/dev.ps1，直接运行包内 EXE，不需要 Go 源码或 Go 编译器。源码目录仍按原方式编译。首次没有 OAuth 应用配置时仍需 Python 准备锁定配置；之后日常启动不调用 Python/pip。


Linux 同机部署可使用 deploy/compose.yml：host 网络默认只监听 127.0.0.1:5722，供同机 PicHub 调用，并可访问宿主机 127.0.0.1:7890 代理。管理页可经 SSH 本地转发访问；此模板不创建公网反向代理。准备 data/curator.db、data/private/accounts 及 private/admin-token、private/oauth.json，默认文件给 UID 65532 读取，数据库目录可写；也可设置 PIXIV_CURATOR_UID / PIXIV_CURATOR_GID 为拥有该目录的非 root 宿主机用户，保留 0700/0600 权限而无需将凭据开放给其它用户。跨系统迁移时 account.token_file 必须改成容器内实际绝对路径；不要直接复制 Windows 路径或运行中的 WAL 文件。

设置 PIXIV_CURATOR_IMAGE 为要部署的版本或 digest。若使用 Actions 的 GHCR 镜像部署归档，先验证 SHA256SUMS / 镜像 ID / revision，再 docker load；相应 Compose 环境可设置 PIXIV_CURATOR_PULL_POLICY=never，避免没有 GHCR 凭据时重复拉取。普通 registry 拉取使用 missing 或 always，并自行配置只读包权限。

Compose 将整个 private 目录只读挂载到 /run/secrets；该目录必须是运行用户拥有的 0700 目录，内部凭据为 0600。不要改回分别挂载凭据文件，否则 Docker 自动创建的父目录权限可能被程序拒绝。PIXIV_CURATOR_ADDR 可覆盖默认监听地址；更改端口时也要同步 PUBLIC_BASE_URL。

同机可达不代表消费方允许内网 URL。若 PicHub 的探测器启用 SSRF 防护，127.0.0.1 或私网地址可能被拒绝；需在消费方既有安全策略内提供允许的服务地址，不应关闭防护。SSH 管理访问示例：`ssh -N -L 5722:127.0.0.1:5722 -p 222 l31155@ut572.eu.org`，浏览器打开 `http://127.0.0.1:5722/admin/login`。同一账号凭据迁往服务器后应停止旧本机实例，避免并发刷新；旧 Refresh Token 可能已轮换，不能直接用旧副本覆盖服务器新凭据。


## 当前公网与 PicHub 配置（2026-11-12）

Lucky 已将 https://pix.ut572.eu.org 反代到服务器 127.0.0.1:5722；部署 .env 的 PIXIV_CURATOR_PUBLIC_BASE_URL 设置为该 HTTPS origin。公开 JSON 的 url 指向相同域名固定图片页，original_url 仅作原图兼容。管理页使用 https://pix.ut572.eu.org/admin/login；修复版根路径自动跳到登录页，已有会话直接进入管理页。HTTPS 配置启用 Secure Cookie，Cookie 写操作的 Origin 必须匹配此域名。

PicHub 已新增 Pixiv Curator 图源（ID 40）：URL 为 https://pix.ut572.eu.org/random、resp_type=json、json_path=url、权重 10、分类 pixiv-curator；无需配置 Pixiv Referer 或任何 Curator/Pixiv 凭据。单独调用可用 `https://hub.ut572.eu.org/random?category=pixiv-curator&format=json`。实际分发已生成 PicHub 本地文件并完整返回 200 图片；没有验证收藏流程。既有 SSRF 规则保持启用，未改 PicHub 源码。


2026-11-12 后续联调：当前 PicHub 图源为 ID 41，URL `https://pix.ut572.eu.org/random?format=json&delivery=proxy`，未配置分类。此前 ID 40 / `category=pixiv-curator` 的示例仅对应当时状态，当前按该分类请求会返回 no available sources。当前图源结构探测通过；本次未修改 PicHub 图源配置。
