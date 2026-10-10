# 运行与 PicHub 接入

本轮已通过离线/mock 测试、本机二进制 HTTP 冒烟与跨平台编译；真实 Pixiv 账号、CDN 出口、PicHub 下载/显示/收藏和新版本容器尚未验收。以下是接入步骤，不能作为实网通过记录。

## 一键本机联调

运行 `./scripts/dev.sh` 自动准备可选 gppt 登录依赖和私密配置，并启动管理页面/API。在管理页点击「打开 Pixiv 登录窗口」即可使用账号登录；不需要手工提取 refresh token 或填写 OAuth 参数。说明见 [pixiv-login.md](pixiv-login.md)。没有桌面或只做离线检查时使用 `./scripts/dev.sh --no-login-helper`。下文保留手动部署方式。

## 本机首次启动

需要 Go 1.26+。先执行 `make check build`。在仓库外建立私密目录（POSIX 0700），生成随机管理 token 文件（0600），勿把真实内容写进命令行历史。设置以下环境变量后运行 `./bin/pixiv-curator`：

```sh
export PIXIV_CURATOR_ADMIN_TOKEN_FILE=/absolute/private/admin-token
export PIXIV_CURATOR_PIXIV_TOKEN_FILE=/absolute/private/pixiv-refresh-token
export PIXIV_CURATOR_OAUTH_CONFIG_FILE=/absolute/private/oauth.json
export PIXIV_CURATOR_DATABASE_PATH=/absolute/data/curator.db
export PIXIV_CURATOR_PUBLIC_BASE_URL=http://127.0.0.1:5722
```

管理 token 和 refresh token 是纯文本，允许末尾换行。OAuth 文件为 JSON，字段 `client_id`、`client_secret` 必填，`hash_secret` 按应用协议提供；全部采用私密文件权限。Go 程序不内置 OAuth 应用凭据；一键脚本从已安装 gppt 的客户端配置生成匹配私密文件。refresh token 文件可先不存在，在 `/admin/login` 登录后的账号页面写入；其父目录必须可写，更新会原子替换文件。OAuth 配置变更后重启服务。

1. 打开 `/admin/login`，使用管理 token 建立短期会话。
2. 在账号页保存 refresh token 并发起关注同步，查看后台任务状态。
3. 在画师页勾选确认，或输入 UID 后明确批准。关注不会自动变成采集许可。
4. 查看作品预览和任务完成状态；首次采集、补全均在后台进行。
5. 启动推荐，记录返回的 run_id，刷新候选并预览，明确批准后才加入供图范围。
6. 保存全局筛选规则，按作品设置喜欢/排除。默认 safe、non_ai；未知分类始终排除。

`paused_auth` 表示账号未配置或认证需要修复；保存凭据后显式恢复任务。429/网络错误有限重试，永久失败可手动重新排队。周期采集每 6 小时扫描已批准且允许采集的画师；暂停采集保留现有供图，屏蔽立即停止新供图。取消关注不删除目录。

## 容器与网络

构建流程见 [发布文档](releasing.md)。容器使用 UID 65532；挂载 `/data` 持久保存数据库，单实例运行，确保可写。管理 token/OAuth 文件可只读挂载；refresh token 的**整个私密父目录**需可写挂载以支持原子轮换，不能只读挂载这个单文件。主机权限与容器 UID 对应，勿将凭据放进镜像。

`PUBLIC_BASE_URL` 必须同时满足管理页面访问 Origin 和 PicHub 返回图片 URL 可达的要求，监听地址由 `ADDR` 单独设置。反向代理可提供共同可达的域名；HTTPS 下 Cookie 启用 Secure。供图接口当前无独立服务鉴权，只部署到受控本机/内网，公网接入需要单独设计。

图片出口直连固定 `i.pximg.net:443`，无任意 URL/代理配置，不转发账号 token、Cookie 或 Range，不建立原图缓存。需要确认当前网络可达 Pixiv OAuth/App API 与图片 CDN。

## PicHub 图源配置

在 PicHub 中新增 JSON 图源，按其实际配置界面的字段填写：

| 字段 | 值 |
| --- | --- |
| URL | `http://实际可达主机:5722/random?format=json&collection=preferred` |
| 响应类型 | JSON |
| 图片路径 | `url` |
| proxy_mode | 开启 |
| Headers | 不放 Pixiv 或 Curator 管理凭据 |

如果 PicHub 在容器中，`127.0.0.1` 指向它自己，不能指向主机 Curator；使用共同网络内的服务地址或共同域名，并同步设置 Curator 的 `PUBLIC_BASE_URL`。不要把这里的示例当作已经发布的图源配置文件格式。

验收时使用独立测试目录：请求 JSON，重复访问同一 `/i/{id}/{page}` 确认身份不变，检查 PicHub 下载、显示和收藏；再验证预取和重复请求不会写入作品喜欢。空目录返回 503 `CATALOG_EMPTY`，过滤后无结果返回 404 `NO_MATCH`，非法参数返回 422。小图库可能返回 `repeated=true`，内容排除规则不会放宽。

本轮未改动 PicHub 仓库或配置，也未执行真实收藏、关注等账号写操作。

## 备份与恢复

最简方式：停止服务，备份数据库及私密凭据目录，再恢复到独立路径，用相同版本启动检查 `/readyz`、画师、作品与任务。运行中的数据库应使用 SQLite `.backup`；代码中的 `Store.Backup` 使用 `VACUUM INTO` 一致性快照（目标必须不存在），该行为及恢复已由临时 SQLite 测试覆盖，目前无独立备份 HTTP/CLI 入口。

不要运行中仅复制 `.db`，不要多实例写同库。迁移前备份；新版本迁移只前进，旧版本遇到未知 schema 拒绝启动。恢复凭据时重设 0700/0600（Windows 使用受限 ACL），会话不恢复，重新登录；未完成任务待租约过期后恢复，数据与游标保持事务一致。
