# Pixiv Curator

以 Go 为主的个人 Pixiv 图库目录与选图服务，向 PicHub-Aggregator 提供图片来源。

目标流程：同步关注画师 → 推荐候选 → 预览并手动确认 → 自动增量采集作品目录 → 按偏好选图 → PicHub 下载、缓存、收藏和分发。

## 当前状态

已实现 Go 服务、SQLite 事务迁移与一致性备份、画师/作品/分页/标签/设置存储、持久任务队列与单 Worker、管理 Bearer/短期会话鉴权、画师手动导入及审核 API、任务列表/取消和本地就绪检查。

**尚未实现** Pixiv 认证与实网采集、推荐、随机选图、图片转发或管理界面。批准画师会原子创建采集任务，当前 Worker 将其暂停为 `paused_auth / ACCOUNT_NOT_CONFIGURED`。当前不会连接 Pixiv，也不会下载图片。

## 技术栈

- Go 1.26+；HTTP 使用标准库 net/http。
- SQLite 使用纯 Go `modernc.org/sqlite v1.60.1`，WAL、外键检查、单写连接；迁移嵌入二进制，拒绝未知版本。
- 计划同一代码库提供 API 与后台 Worker，共用目录数据。
- 原图下载、磁盘缓存和收藏由 PicHub 承担。
- 管理页面后续按需求选择模板或独立前端；核心业务保持 Go 实现。

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

```sh
PIXIV_CURATOR_ADDR=127.0.0.1:8080 PIXIV_CURATOR_LOG_LEVEL=debug go run ./cmd/api
```

运行时数据预留在 data/，已忽略 Git 跟踪。真实 token、代理凭据和 .env 不得提交。

管理 token 文件应位于权限 0700 的目录，文件权限 0600；Windows 使用受限 ACL（应用不校验 POSIX 权限位）。管理 API 接受 `Authorization: Bearer ...`，浏览器通过 `POST /admin/login` 提交 JSON token 建立短期会话，Cookie 写操作同时要求匹配 Origin 和 `X-CSRF-Token`。会话重启失效，`POST /admin/logout` 撤销会话。勿将 token 放入 URL、日志或 localStorage。

当前管理 API：`GET/POST /admin/api/artists`、`POST /admin/api/artists/{id}/{approve|reject|block|restore}`、`PATCH /admin/api/artists/{id}`（collection_enabled）、`GET /admin/api/jobs`、`POST /admin/api/jobs/{id}/cancel`。Pixiv ID JSON 使用字符串，列表采用 cursor，默认 50、最多 200。`GET /readyz` 检查本地数据库与 Worker；不表示 Pixiv 连通或目录有图。

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

发布约定与操作见 [构建与完整发布](docs/releasing.md)。完整业务流程仍在开发。

## Git

使用 main 分支进行版本管理。

- origin：git@github.com:Untitled572/pixiv-curator.git
- Go module：github.com/Untitled572/pixiv-curator
- 本地提交完成后，仅在用户要求时推送到远端。

未列入当前状态的设计接口仍为计划；实际验收与限制见开发计划。
