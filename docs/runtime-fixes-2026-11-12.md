# D 盘同步与运行修复（2026-11-12）

## 完成情况

H 盘源码及本地 Git 开发历史已快进同步到 D 盘，以 f21d010 为修复基线；同步时逐文件校验 122 个源码/文档文件，并带入未提交检查文档。没有复制原运行库、原账号凭据和构建目录，D 盘已有 private 目录保留。后续业务代码与文档修改全部发生在 D 盘；H 盘未继续开发。未创建远端、推送或发布版本，修复保留为可审阅的未提交差异。

本轮已完成关注默认头像、Windows SQLite URI、Windows 令牌测试、管理预览防盗链、多页预览缺失和重试成功后旧错误码残留的修复。使用用户授权的专用测试账号，Windows 原生实网链路从账号验证、全量关注同步、限定画师采集、选图到推荐及样本预览均通过。

## 改动

- [头像校验](../internal/domain/media.go)额外接受官方静态默认头像 s.pximg.net/common/images/no_profile.png；[画师归一化](../internal/pixiv/models.go)使用独立头像规则，原图白名单不扩大。默认头像不再导致整页关注同步失败。
- [SQLite 连接](../internal/store/sqlite/store.go)正确编码 Windows 盘符和空格、中文、#、% 等路径。真实开库、写入、关闭和重开回归通过；Unix 保持原行为。
- [令牌测试](../internal/pixiv/client_test.go)按平台检查 POSIX 权限位，用目录占据凭据路径模拟持久化失败，避免依赖 Windows 符号链接特权。仍验证轮换保存失败后停止后续刷新。
- 经用户明确同意，仅[鉴权管理预览](../internal/httpapi/images.go)恢复带 Pixiv Referer 的流式转发。使用现有[媒体边界](../internal/media/proxy.go)，不落盘、不携带账号或管理凭据，固定目标来自数据库，逐跳校验，超时 20 秒、最多 64 MiB。HEAD 不读图片正文，断流中止响应，403/404 去重安排同作品修复。
- [管理页面](../internal/admin/static/admin.js)使用同源默认流式预览。多页作品第一张缩略图缺失时复用作品级首图预览；[固定预览解析](../internal/store/sqlite/selection.go)缺缩略图时回退同一页原图，不伪造其他分页。
- [任务原子完成](../internal/store/sqlite/artist_actions.go)成功时清空旧 error_code，避免重试已经成功的账号仍显示 NETWORK_ERROR。租约校验、状态事务及有界重试保持不变。
- README、开发规范、设计、架构及运行文档更新预览例外和 Windows 原生启动方式。公开 /random 和 /i 继续只返回原图 URL 或 302，消费端仍需自己的网络及 Pixiv Referer；没有扩大成公开图片代理。

## 验证

| 检查 | 结果 |
| --- | --- |
| Windows go vet ./... | 通过 |
| Windows go test -race ./... | 最终全仓通过 |
| Fedora WSL go vet ./... / go test -race ./... | 最终全仓通过 |
| Linux / Windows × amd64 / arm64，CGO_ENABLED=0 | 四个目标最终构建通过 |
| Windows 原生及 Linux 实际启动 | /healthz、/version、/readyz 与管理鉴权冒烟通过 |
| 格式与语法 | gofmt 无待格式化文件；管理 JavaScript 语法、git diff --check 通过 |
| 管理预览边界 | mock 覆盖认证、Cookie/Bearer 凭据不转发、固定页、未知 URL 参数拒绝、待确认样本公开隔离、HEAD、失败修复去重和断流 |
| 默认头像与路径回归 | 覆盖默认头像的完整关注快照、头像/原图白名单分离、多页缺缩略图及特殊字符数据库路径持久重开 |
| 错误恢复回归 | NETWORK_ERROR 后重试成功时旧错误码清空；旧租约不可继续写入的既有测试通过 |

Windows 竞态检查中出现过一次临时测试目录清理失败；相关账号包随后连续 5 轮通过，后续全仓竞态检查也通过。未据此添加重试或延时来掩盖问题，尚未确定该次清理失败的环境根因。

## Windows 原生实网结果

使用全新、Git 忽略且通过 NTFS ACL 仅授权当前用户的验证目录，管理 token、OAuth 应用配置和测试账号轮换凭据均不进入本报告或源码。没有使用原正式账号的 Refresh Token，没有对 Pixiv 执行关注、取关或收藏写操作。

1. OAuth 添加账号返回 200，Pixiv 身份验证成功，状态 ready。
2. 完整同步公开及私密关注共 117 位画师。最终二进制复验为 5 页、117 项，任务 succeeded，任务和账号 error_code 均为空，账号/画师关系没有重复。
3. 为限定实网请求量，仅保留一位画师的自动近期采集，其余采集在可丢弃验证库中暂停。近期任务 succeeded，取得 114 件作品、118 个图片分页；没有把这次抽样说成 117 位画师历史图库全部采集完成。
4. 默认预算推荐 succeeded，应用层预算请求计数 150，生成 60 位候选，60 位均有作品样本并保留 pending。没有自动批准推荐。
5. /random 返回 200 与 Pixiv 原图 URL；连续请求同一 /i 地址返回相同图片身份和 URL。
6. 浏览器会话登录返回 200 并建立 Cookie；不带 Bearer、仅使用会话 Cookie 获取待确认候选的管理预览，HEAD 返回 200 image/jpeg 且无正文，GET 返回 200 image/jpeg，检查前 4096 字节后关闭。未保存图片，未转发 CDN Set-Cookie。
7. 验证服务均已停止。最终 Windows 可执行文件位于已忽略的 bin/pixiv-curator.exe，原生启动配置方法见[运行文档](running.md#windows-原生启动)。

初次 Windows 实网请求遇到代理 TLS EOF，随后恢复；后台有限重试成功继续同步。网络波动不等于认证参数或代码永久失效，也不能保证部署网络始终可用。

## 尚未包含

- 未验收全部画师历史目录；批量采集规模和耗时需另行测量。
- 未重新进行真实浏览器截图/布局和实体手机验收；本轮验证的是真实管理会话与预览 HTTP 链路。
- 没有修改 PicHub，也未验证其下载、显示、收藏；公开原图 URL 仍要求消费端配置 Referer。
- 未执行 Docker 或远端 GitHub Actions，本次不是完整发布。
