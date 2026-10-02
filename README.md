# Pixiv Curator

以 Go 为主的个人 Pixiv 图库目录与选图服务，向 PicHub-Aggregator 提供图片来源。

目标流程：同步关注画师 → 推荐候选 → 预览并手动确认 → 自动增量采集作品目录 → 按偏好选图 → PicHub 下载、缓存、收藏和分发。

## 当前状态

已完成项目初始化：Go module、HTTP 服务、环境配置、结构化日志、超时与优雅退出、健康检查、版本接口、基础测试和开发文档。

**尚未实现** Pixiv 认证、数据库、关注同步、推荐、作品采集、随机选图或管理界面。当前不会连接 Pixiv，也不会下载图片。

## 技术栈

- Go 1.25+；初始版本仅使用标准库（net/http、log/slog 等）。
- 计划使用 SQLite 保存画师、作品、分页链接、反馈与任务；驱动和迁移在数据库阶段引入。
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

```sh
PIXIV_CURATOR_ADDR=127.0.0.1:8080 PIXIV_CURATOR_LOG_LEVEL=debug go run ./cmd/api
```

运行时数据预留在 data/，已忽略 Git 跟踪。真实 token、代理凭据和 .env 不得提交。

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

## Git

使用 main 分支进行版本管理。

- origin：https://github.com/Untitled572/pixiv-curator.git
- Go module：github.com/Untitled572/pixiv-curator
- 本地提交完成后，仅在用户要求时推送到远端。

设计文档中的接口和业务模块均为计划，不表示已经可用。
