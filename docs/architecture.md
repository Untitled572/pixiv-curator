# 架构与边界

## 目标

独立的个人 Pixiv 目录与选图服务。核心采用 Go，原图文件生命周期由 PicHub-Aggregator 管理。

用户已确认：
1. 推荐新画师时先展示作品预览，手动确认后自动采集。
2. 当前保存 ID、元数据和链接，不做原图全量归档。
3. 原图下载交给 PicHub。

## 计划流程

同步已关注画师 → 生成推荐候选 → 人工确认 → 本地采集名单 → 后台增量采集作品和分页链接 → 规则筛选 → API 返回固定图片地址 → PicHub 下载与分发。

本地确认不等于在 Pixiv 上关注。取消远端关注不得隐式删除已收集的作品目录；数据保留策略需要单独实现。

## 计划模块

| 模块 | 责任 |
| --- | --- |
| Pixiv 客户端 | OAuth 刷新、关注列表、作品分页、详情；统一超时与错误分类 |
| 画师管理 | 关注导入、候选、确认、拒绝、屏蔽和采集状态 |
| 推荐 | 从关注标签与作品统计构建画像、召回和排序 |
| 目录 | 作品元数据、每页图片 URL、可用状态 |
| 任务 | 增量同步、重试退避、限速、失败恢复、链接修复 |
| 选图 | 偏好过滤、作者均衡、近期去重、稳定图片地址 |
| HTTP API | 管理接口、供图接口及必要的图片流转发 |
| 数据库 | SQLite、显式迁移；初期限制写并发 |

初始化只实现 config 和 HTTP 基础设施，以上业务模块将在需要时创建。初期不引入 Redis、微服务拆分、多账号大代理池或额外选图引擎。

## 数据模型方向

- Artist：Pixiv UID、名称、来源、候选/确认/屏蔽状态、同步时间。
- Artwork：作品 ID、画师 UID、标题、标签、作品类型、AI/R18 标记、热度、入选状态。
- ImagePage：作品 ID + page_index 唯一；原图/预览 URL、尺寸、链接可用状态。
- Feedback：画师或作品级反馈，区分喜欢、拒绝、屏蔽和普通曝光。
- Job：任务类型、输入、状态、重试次数、游标、错误摘要。

上游未知的 AI/R18 标记不得默认当作已确认的非 AI/全年龄。采集时已取得的元数据直接复用，避免为了导入另一个模块再次调用详情 API。

## 拟定的 PicHub 契约（未实现）

```http
GET /random?format=json&collection=preferred
```

```json
{
  "url": "https://curator.example/i/12345678/0",
  "illust_id": "12345678",
  "page_index": 0,
  "artist_id": "876543"
}
```

PicHub 配置 resp_type=json、json_path=url，并启用 proxy_mode。当前通用图源适配器只需要 url，其余标识为未来作品反馈预留；不会自动被 PicHub 保存。

固定图片地址不能再次随机选图。图片端点可以根据目录的原图链接带 Pixiv Referer 流式转发，不把原图落盘。若改为直接返回 pximg URL，则 PicHub 必须自行满足网络与请求头条件。

API 选图只读取本地目录；不得在前台执行全量采集、刷新关注或推荐计算。后台负责目录更新和失效链接修复。

## PicHub 集成限制

核查基线：PicHub main 562be65092a4c89dbf869c5e957d634f70e83f2f。

- 基础 JSON 图源接入无需修改 PicHub 核心。
- 当前 like/dislike 调整图源整体权重，不构成画师或作品反馈。
- 下载使用随机文件 ID，尚无作品 ID/URL/内容级去重；本项目需近期选图去重，后续可扩展 PicHub 复用缓存。
- 图源 Headers 可经读取接口返回，不能放入 Pixiv refresh token。Pixiv 凭据只在本项目内部使用，服务间鉴权在实际接入阶段单独设计。
- PicHub 会预取，抓取次数不等于用户观看次数。

## 参考代码与实现方式

- pixiv-downloader：参考认证、关注同步、索引、重试。
- pixiv-artist-recsys：参考候选召回、标签画像和启发式排序。
- Random-image-api：参考目录字段、筛选参数和供图契约。
- pixivcat-backend：参考带 Referer 的图片字节转发。

现有 Python 项目是参考实现，不作为必须运行的服务。Go 版本按模块实现并测试，不直接合并六套应用或共享各自的运行数据库。

参考站：https://i.mukyu.ru/docs
接入方：https://github.com/Untitled572/PicHub-Aggregator
