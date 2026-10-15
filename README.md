# Pixiv Curator

以 Go 为主的个人 Pixiv 图库目录与选图服务，向 PicHub-Aggregator 提供图片来源。

目标流程：同步关注画师 → 推荐候选 → 预览并手动确认 → 自动增量采集作品目录 → 按偏好选图 → PicHub 下载、缓存、收藏和分发。

## 当前状态

已实现单进程 Go 服务与管理页面：SQLite 目录、管理鉴权、持久任务、Pixiv OAuth 刷新与关注/作品分页采集、元数据补全、候选推荐与人工审核、偏好选图、固定图片页转发。

以上通过离线/mock 测试及本机 HTTP 验证，**尚未完成真实 Pixiv/PicHub 联调与本轮 Docker 构建验收**。未配置账号时任务暂停为 `paused_auth`，不会把推荐候选自动批准。原图仅流式转发，不写磁盘。

## 技术栈

- Go 1.26+；HTTP 使用标准库 net/http。
- SQLite 使用纯 Go `modernc.org/sqlite v1.60.1`，WAL、外键检查、单写连接；迁移嵌入二进制，拒绝未知版本。
- 同一二进制提供 API、后台 Worker、定时维护与内嵌 Go 模板页面。
- 原图下载、磁盘缓存和收藏由 PicHub 承担。
- 管理页面使用 Go 模板和原生 JavaScript，无额外前端构建。

## 一键启动与 Pixiv 账号登录

```sh
./scripts/dev.sh
```

脚本准备 Python OAuth 依赖与私密配置，构建并启动同端口的管理页面与 Go API。终端显示管理 token 的文件路径；登录管理页后进入「Pixiv 账号」，点击「打开 Pixiv 登录」，在普通浏览器中完成 Pixiv 登录，再从浏览器 Network 请求中复制授权 code 回管理页提交。

需要 Go 1.26+、Python 3.10+（含 venv/pip）；登录不需要 Chromium 或服务端桌面。默认私密开发目录为 `data/dev`。仅离线查看页面/API 可运行 `./scripts/dev.sh --no-login-helper`。登录步骤与限制见 [账号登录与本机联调](docs/pixiv-login.md)。

## 开始开发

```sh
go test ./...
go run ./cmd/api
```

默认地址：http://127.0.0.1:5722

```sh
curl http://127.0.0.1:5722/healthz
# {"status":"ok"}

curl http://127.0.0.1:5722/version
# {"name":"pixiv-curator","version":"dev"}
```

healthz 当前只表示 HTTP 进程存活，不代表 Pixiv 连通、数据库就绪或作品可用。

## 配置

通过环境变量配置，程序不会自动读取 .env。示例见 .env.example。

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| PIXIV_CURATOR_ADDR | 127.0.0.1:5722 | HTTP 监听地址 |
| PIXIV_CURATOR_LOG_LEVEL | info | debug / info / warn / error |
| PIXIV_CURATOR_DATABASE_PATH | data/curator.db | 本地 SQLite 文件，启动时执行版本迁移 |
| PIXIV_CURATOR_PUBLIC_BASE_URL | http://127.0.0.1:5722 | HTTP(S) origin；浏览器管理写操作校验此 Origin |
| PIXIV_CURATOR_ADMIN_TOKEN_FILE | 未配置 | 私密管理 token 文件；不配置时所有管理操作拒绝 |
| PIXIV_CURATOR_PIXIV_TOKEN_FILE | 未配置 | refresh token 私密文件，轮换后原子写回；所在目录必须可写 |
| PIXIV_CURATOR_OAUTH_CONFIG_FILE | 未配置 | OAuth 应用参数私密 JSON 文件；一键脚本自动生成匹配配置 |
| PIXIV_CURATOR_LOGIN_PYTHON | 未配置 | 保留配置项：旧版本机登录 helper 的 Python 绝对路径 |
| PIXIV_CURATOR_LOGIN_HELPER | 未配置 | 保留配置项：旧版 `scripts/pixiv-login.py` 绝对路径 |

```sh
PIXIV_CURATOR_ADDR=127.0.0.1:8080 PIXIV_CURATOR_LOG_LEVEL=debug go run ./cmd/api
```

运行时数据预留在 data/，已忽略 Git 跟踪。真实 token、代理凭据和 .env 不得提交。

管理 token 文件应位于权限 0700 的目录，文件权限 0600；Windows 使用受限 ACL（应用不校验 POSIX 权限位）。管理 API 接受 `Authorization: Bearer ...`，浏览器通过 `POST /admin/login` 提交 JSON token 建立短期会话，Cookie 写操作同时要求匹配 Origin 和 `X-CSRF-Token`。登录可提交 `remember: true` 记住 30 天；未勾选时为最长 12 小时的浏览器会话。会话重启失效，`POST /admin/logout` 撤销会话。勿将 token 放入 URL、日志或 localStorage。

打开 `/admin/login` 登录，然后在账号页使用普通浏览器完成 Pixiv 登录，或手动保存 refresh token，再同步关注；在画师页明确勾选并批准采集，在推荐页启动推荐批次、查看预览并确认。任务入队并不表示采集完成；缺失或失效凭据需修复后显式恢复暂停任务。

供图：`GET /random?format=json&collection=preferred` 返回 `url`、作品/页身份、尺寸与 `repeated`；`GET/HEAD /i/{illust_id}/{page_index}` 固定该页身份。默认只选择已批准、已知安全、非 AI 的静态作品；请求只能收紧全局规则。`balanced` 先均衡画师，再在画师内优先喜欢作品；`random` 保持随机。近期去重不足时只放宽去重，不放宽排除。

管理 API、筛选参数和错误码见 [详细设计](docs/detailed-design.md)。`GET /readyz` 检查本地数据库与 Worker，不表示 Pixiv 连通或目录有图。首次账号、容器、备份恢复和 PicHub 图源配置见 [运行与接入](docs/running.md)。

备份应停机后完整保存数据库，或使用 SQLite `.backup`；不要在运行期间只复制主 `.db`。库内 `Store.Backup` 使用 `VACUUM INTO` 生成包含 WAL 已提交数据的一致性快照，目标必须不存在。凭据独立备份。

## 常用命令

```sh
make fmt
make check
make build
./bin/pixiv-curator
```

make check 运行 go vet 与 go test -race，需要支持 race detector 的本地 C 工具链；基础测试可独立运行 go test ./...。

发布时可以注入版本：

```sh
go build -trimpath -ldflags "-X main.version=v0.1.0" -o bin/pixiv-curator ./cmd/api
```

## 目录

```text
cmd/api/              API 进程入口
internal/config/      环境变量与配置校验
internal/httpapi/     HTTP 路由与响应
docs/architecture.md     架构概要与文档导航
docs/detailed-design.md  数据、任务、状态与接口详细设计
docs/development-plan.md 开发任务、依赖、状态与验收
docs/roadmap.md          里程碑导航
```

## 开发文档

- [AGENTS.md](AGENTS.md)：仓库开发规范。
- [详细设计](docs/detailed-design.md)：数据模型、任务状态机、推荐流程、HTTP 契约与 PicHub 边界。
- [开发计划](docs/development-plan.md)：任务编号、依赖、当前状态与验收标准。
- [架构概要](docs/architecture.md)：快速了解项目结构。

## 构建与发布

GitHub Actions 在普通提交/PR 时执行测试、Linux/Windows 构建和 Docker 验证；推送版本标签后生成完整 Release 和 GHCR 双架构镜像。

发布约定与操作见 [构建与完整发布](docs/releasing.md)。实网接入与本轮容器验证仍待完成。

## Git

使用 main 分支进行版本管理。

- origin：git@github.com:Untitled572/pixiv-curator.git
- Go module：github.com/Untitled572/pixiv-curator
- 本地提交完成后，仅在用户要求时推送到远端。

未列入当前状态的设计接口仍为计划；实际验收与限制见开发计划。
