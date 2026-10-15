# 架构概要

更新：2026-10-15。代码状态、未完成验收和任务清单以[开发计划](development-plan.md)为准。

## 运行边界

Pixiv Curator 是单进程 Go 服务，使用 SQLite 保存目录、任务、偏好和多个 Pixiv 账号的本地关联。后台 Worker 执行关注同步、作品采集、推荐和 URL 修复；管理页面只发起操作并展示结果。HTTP handler 负责鉴权、参数解析和响应，业务流程位于应用服务，Pixiv I/O 和 SQLite 分别位于客户端与存储边界。

三个管理页面是“画师图库”“发现画师”“设置”。当前没有任务面板；任务状态、阶段、稳定错误码由结构化服务日志记录。关注同步发现的画师自动批准、启用采集并入队，`blocked` / `rejected` 状态保留。推荐候选先预览，只有用户明确加入才进入采集名单。屏蔽立即排除供图并停止后续采集。

## 数据流

```mermaid
flowchart LR
  UI[三页管理 UI] --> API[管理 API]
  API --> APP[应用服务]
  APP --> DB[(SQLite)]
  APP --> JOB[持久任务]
  JOB --> WORKER[后台 Worker]
  WORKER --> PIXIV[Pixiv OAuth / App API]
  WORKER --> DB
  CLIENT[浏览器或 PicHub] --> SELECT[/random]
  SELECT --> DB
  SELECT -->|JSON 原图 URL 或 302| PXIMG[Pixiv 图片域名]
```

供图 API 只从本地目录筛选一张稳定的作品页身份，并返回数据库中经过校验的原图 URL；`format=redirect` 返回 HTTP 302。Curator 不下载或代理图像字节，消费方在自己的网络环境中访问图片域名。管理预览也由浏览器读取上游预览 URL。服务端网络设置影响服务发出的 Pixiv API/OAuth 请求，不配置 PicHub 或浏览器的出口。

## 账号与目录

每个账号的 Refresh Token 单独验证 Pixiv 用户 ID；相同 Pixiv ID 不得重复添加，更新凭据不得改变已绑定身份。关注关系用 `account_artists` 记录并去重；同一画师仍只有一套本地元数据和作品目录。取关或删除账号不删除关系历史和作品，账号过滤只显示仍存在的账号；默认供图不受取关影响。旧单账号凭据迁移到 `legacy` 账号，旧数据库中的偏好和图库数据沿用。

## 同步和标签

添加账号通过身份验证后安排首次关注同步。运行中的服务每 6 小时安排一次关注同步与启用采集画师的增量采集；计时器从启动后开始，不持久记录下次触发时间。采集优先扫描近期约 100 件作品，随后以较低优先级补齐历史分页。任务与游标持久化，重启后可恢复。缺省手动操作使用首个账号，不通过切换账号规避限速。旧 `legacy` 账号先以 UID 0 导入，成功同步后绑定真实 UID；绑定前不能增加第二账号。

画师自动标签由近期 100 件作品聚合，按频次取前 8 个。用户可设置本地手动覆盖，包括明确清空；删除覆盖后恢复自动标签。图库按标签与账号筛选不改变 Pixiv 元数据或账号关注状态。

## 主要模块

| 目录 | 职责 |
| --- | --- |
| `cmd/api` | 配置、迁移、依赖装配、Worker 和 HTTP 服务生命周期 |
| `internal/domain` | 画师、作品、任务、账号和选图规则 |
| `internal/app` | 采集、推荐、偏好、选图及维护用例 |
| `internal/store/sqlite` | 事务、持久化、迁移和查询 |
| `internal/pixiv` | Pixiv OAuth Refresh Token 与 App API 客户端 |
| `internal/accounts` | 多账号凭据、身份绑定和按账号路由 |
| `internal/jobs` | 持久队列、租约、取消和有限重试 |
| `internal/httpapi` | 管理/供图 API、鉴权、参数校验 |
| `internal/admin` | 内嵌 Go 模板、CSS 和 JavaScript |

架构契约与错误码以[详细设计](detailed-design.md)为准。离线测试通过不代表真实 Pixiv 或 PicHub 网络验收通过。
