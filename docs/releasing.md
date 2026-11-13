# 构建与完整发布

每次完整发布必须包含 Linux、Windows 二进制与 Docker 镜像，由 GitHub Actions 构建。

## 平台与产物

| 目标 | 架构 | 产物 |
| --- | --- | --- |
| Linux | amd64、arm64 | 两个 .tar.gz |
| Windows | amd64、arm64 | 两个 .zip，内含 .exe 与 start.cmd / PowerShell 启动器 |
| Docker | linux/amd64、linux/arm64 | GHCR 多架构镜像 |
| 校验文件 | 全部二进制包 | SHA256SUMS |

二进制采用 CGO_ENABLED=0，后续 SQLite 驱动必须保持跨平台纯 Go 构建能力，或在变更中同步更新全部构建矩阵。Windows amd64 和 Linux amd64 执行原生测试及 HTTP 冒烟；arm64 当前验证交叉编译，未宣称在 arm64 原生机器运行。

## 工作流

### CI

[ci.yml](../.github/workflows/ci.yml) 在 main 推送、Pull Request 和手动触发时运行，也被发布工作流复用：

1. Linux/Windows 原生测试与真实 HTTP 冒烟，Linux 额外执行 race detector。
2. 构建四个二进制压缩包与 SHA256SUMS，作为 Actions artifact 保存 14 天。
3. 构建并启动 Docker amd64 镜像，核验 health/version；另构建 amd64/arm64 镜像。
4. CI 不推送镜像，不创建 Release。

### Release

[release.yml](../.github/workflows/release.yml) 在推送 v* 标签时触发，标签必须符合 vMAJOR.MINOR.PATCH 或带预发布后缀的格式。

1. 校验标签格式，再复用 CI；全部测试、四个平台包和 Docker 验证成功后继续。
2. 使用 GITHUB_TOKEN 推送 GHCR 双架构镜像。
3. 下载并校验四个压缩包，创建或恢复 GitHub Release 草稿并上传全部资产。
4. 镜像和二进制均就绪后才发布 Release；说明中记录镜像 digest。
5. 带后缀的版本标记 prerelease；稳定版本标记最新并更新镜像 latest。

镜像路径：ghcr.io/untitled572/pixiv-curator。首个已发布版本为 v0.1.0；当前稳定版为 v0.1.1，镜像标签为 :v0.1.1 与 :latest。

不得为旧版本补发标签以更新 latest；补发旧版本时应先调整发布策略，避免覆盖当前稳定版本。已发布版本不覆盖，使用新版本号发布修复。

GitHub Release 与 GHCR 无跨系统事务：若镜像已推送而 Release 上传失败，工作流仍算失败；可重跑失败任务，恢复草稿。不能以“镜像存在”认定完整发布成功。

## 权限与私有仓库

- 目前仓库为私有，Actions 已启用。
- 顶层默认 contents: read；仅镜像发布 job 申请 packages: write，Release job 申请 contents: write。
- 使用自动提供的 GITHUB_TOKEN，无需将本机 gh token 写入 Secrets。
- 初次发布后的 GHCR 包可见性需要按实际设置检查，不自动改为公开；私有镜像的拉取方需自行认证。
- Actions 固定到已核对的 commit SHA，升级时统一审查。
- 本机 gh 已登录不等于 CI 有同样权限；CI 权限由 workflow 和仓库策略共同决定。

## 本地验证

要求 Go 1.26+、Python 3.11+；Docker 双架构验证需要 Buildx。

```sh
make check build
python3 scripts/build-release.py --version dev
(cd dist && sha256sum --check SHA256SUMS)
python3 scripts/smoke-test.py --binary ./bin/pixiv-curator --version dev
docker buildx build --load --platform linux/amd64 -t pixiv-curator:dev .
docker run --rm -p 127.0.0.1:5722:5722 pixiv-curator:dev
```

make check build 会检查代码并生成 bin/pixiv-curator。CI 不依赖本地 dist/bin。

Docker 使用非 root UID 65532、scratch 运行层，包含 CA 证书；镜像内监听 0.0.0.0:5722，主机示例仅绑定 127.0.0.1。数据库默认 `/data/curator.db`，持久挂载 `/data` 并确保 UID 65532 可写；镜像内空数据目录已设置此所有者。管理凭据独立只读挂载，目录 0700、文件 0600 且 UID 65532 可读。不要多实例共用同一数据库。数据库迁移前备份，凭据单独恢复。

## 完整发布操作

以下命令只有在明确要发布版本时执行，示例版本号必须替换为实际版本：

```sh
git status --short
git tag -a v0.1.0 -m "Release v0.1.0"
git push origin main
git push origin v0.1.0
gh run list --workflow release.yml
```

发布前确保 main CI 通过、变更和发布说明已核对。推送工作流或普通提交不等同正式发布，不自动创建版本标签。

完整发布完成检查：
- Release 工作流整体成功。
- Release 中有四个压缩包和 SHA256SUMS。
- 包内 /version 返回对应版本。
- GHCR manifest 同时包含 linux/amd64 和 linux/arm64。
- Release 中记录的 digest 与 GHCR 一致。

## 验证记录

2026-10-02，提交 ad0c4df 的[首次 CI](https://github.com/Untitled572/pixiv-curator/actions/runs/36960290306) 全部通过：Linux/Windows 测试及 HTTP 冒烟、Linux race 检查、四个平台压缩包构建与上传、Docker 双架构构建及 amd64 容器冒烟。

当时本机 gh 2.102.0 已登录 Untitled572，SSH 认证及 main 推送成功；仓库保持私有。当时尚未创建版本标签；首次正式发布结果见后续 2026-11-12 记录。

2026-10-08 的新业务与 SQLite 依赖已通过本机 race/vet、HTTP 冒烟和四平台 CGO=0 构建，但 Docker Hub 拉取超时且本机缺 Buildx。本轮镜像未验证；上述首次远端 CI 仅证明初始化版本，不作为新代码的容器验收。该次运行还提示 Docker Actions 的 Node 20 声明已弃用（由 runner 强制使用 Node 24，运行成功）；后续维护时升级对应 Actions。arm64 尚未执行原生运行测试。

2026-10-10：本轮工作流经 actionlint 验证，CI 增加 Python 登录桥接离线测试与 Linux 启动脚本语法检查。查看远端最近 [CI 36960400585](https://github.com/Untitled572/pixiv-curator/actions/runs/36960400585)，Linux/Windows 测试、四个平台包与 Docker 构建均成功，但对应旧提交 4837f0e，不代表本轮代码通过远端 CI。未创建或推送版本标签。

可选账号浏览器登录面向本机桌面源码联调。默认 Docker 镜像仍为纯 Go/scratch，不含 Python、Chromium 或桌面；容器使用私密 refresh token 配置。二进制包随附登录 helper、锁定依赖和说明，启用它仍需自行准备 Python/浏览器环境；Windows 启动器会区分源码与发行包：源码模式需要 Go；发行包直接运行 EXE，无需 Go，首次 OAuth 配置仍可通过锁定 Python 辅助脚本准备。

发布镜像后，Actions 会核对 GHCR manifest 的 linux/amd64 和 linux/arm64 描述，按 digest 拉取 amd64 镜像并核验源码 revision；另外生成 image-deploy-linux-amd64 artifact（保留 7 天），包含同一 GHCR 镜像的部署归档、校验和、镜像 ID、registry digest 及源码 revision，可在没有 GHCR 长期凭据的服务器上校验后导入。此 artifact 不包含账号配置或凭据，不替代正式 GHCR 发布。

2026-11-12：[v0.1.0 正式发行](https://github.com/Untitled572/pixiv-curator/actions/runs/43169485544) 全部成功，四个 Windows/Linux amd64/arm64 压缩包和校验和已上传至 [Release](https://github.com/Untitled572/pixiv-curator/releases/tag/v0.1.0)。GHCR 双架构 manifest 经按 digest 回拉校验，digest 为 sha256:bd363c124c89db4f16c402f89a4872a74de0f25d1098cab7b2d7dc18017d0df7。服务器已部署此 amd64 镜像，健康/版本、管理登录、统计、公开图片完整读取与后台作品采集通过；arm64 未执行原生运行。Compose 后续修正目录挂载，使用整个 0700 private 目录只读挂载到 /run/secrets，避免 Docker 自动创建的 0755 父目录被凭据校验拒绝；真实 Compose 鉴权回归已通过。

2026-11-12：[v0.1.1 完整发行](https://github.com/Untitled572/pixiv-curator/actions/runs/43179591669) 与 main CI 全部成功，源码 40ed3f7；Windows/Linux amd64、arm64 压缩包及 SHA256SUMS 已发布，Docker 双架构 manifest digest 为 sha256:0c90ee52e0b601d095efbc1e0a4f800aa5489a5f35866a6cc3e80a319c6e5826。服务器已从一致性备份后升级，实际根路径登录前后跳转、管理鉴权、PicHub 分发完整图片以及关注同步 5 页/117 人均通过。arm64 仍只有构建检查，没有原生运行验收。

只读 Compose 根文件系统必须提供可写的内存临时目录：模板为运行 UID/GID 挂载 128 MiB `/tmp`，并设置 `SQLITE_TMPDIR=/tmp`。SQLite 大目录迁移的临时日志可能需要此目录；仅 `/data` 可写不足以覆盖该场景。Compose 冒烟测试使用 12,000 张次页的旧版数据库验证升级，账号与任务为空，不访问 Pixiv。


2026-11-12：[v0.2.1 完整发行](https://github.com/Untitled572/pixiv-curator/actions/runs/43216825294) 成功，源码 c3654f6，Windows/Linux amd64、arm64 二进制和 SHA256SUMS 已发布；GHCR 双架构 digest 为 `sha256:6b9786c7c5073b8b34c772b1e150c73eb9a7cb207c821519168494b68cf7842d`。服务器已校验 GHCR 导出归档、源码 revision 和镜像内容后升级，数据库迁移至 11。Compose 临时目录修复来自后续 98083b4，使用仓库最新模板；其 [CI](https://github.com/Untitled572/pixiv-curator/actions/runs/43222679715) 已通过，不需要重建同一应用镜像。v0.2.0 发布失败且没有部分 Release，保留该标签，不重写历史。arm64 只有构建检查，未做原生运行验收。


2026-11-13：v0.2.2 的[完整发行](https://github.com/Untitled572/pixiv-curator/actions/runs/43236444267) 已成功，随后按用户要求将推荐从 48 减至 30 并补齐缺失样本处理。[v0.2.3 完整发行](https://github.com/Untitled572/pixiv-curator/actions/runs/43341365585) 全部成功，源码 `93fa6c9`，四个 Windows/Linux amd64、arm64 包和 SHA256SUMS 已发布；双架构 GHCR digest 为 `sha256:dd9d80fbc94b6ad667f98ef29f88bb634c2a708be208c83d9565b01736e67561`。服务器已备份并升级至 v0.2.3，30 位推荐全部有样本、暂停任务与接口过滤实测通过。arm64 未执行原生运行。

2026-11-13：[v0.2.4 完整发行](https://github.com/Untitled572/pixiv-curator/actions/runs/43348985885) 成功，源码 986c92b，四个平台包与 Docker 双架构发布通过；GHCR digest `sha256:ea873edffb96d88bb3bef0aab265529e814f918a1fbbe262f90661dd82f4fa83`。服务器已备份升级，实测取消一小时限制、活动任务去重、30 位候选及展示筛选隔离。arm64 仅构建验证。

2026-11-13：[v0.2.5 完整发行](https://github.com/Untitled572/pixiv-curator/actions/runs/43352220491) 成功，源码6207077，四个平台包与双架构镜像已发布。GHCR digest `sha256:96f02a83f17fa4d870b2d14f0d4768ae7fe38642e11a0e06ee7d33e0830605df`；服务器已升级，实网30位均有3件样本，24常规与6探索名单刷新稳定。GUI布局使用隔离浏览器验证，未进行arm64原生运行。

2026-11-13：[v0.2.6 完整发行](https://github.com/Untitled572/pixiv-curator/actions/runs/43355928823) 成功，源码4e6d693；GHCR digest `sha256:f3761e60c262a03ac690e5a372940452fa014631b185a5e1d4d194e75ca79e24`。服务器已备份升级；实网验证阶段百分比及固定多样种子，30候选中出现1位含成人样本。探索批准动画/无专属徽标经隔离浏览器验证；未自动批准任何真实画师。


2026-11-13：[v0.2.7完整发行](https://github.com/Untitled572/pixiv-curator/actions/runs/43378510700) 成功，源码3b34ce9；四个平台二进制及双架构GHCR已发布，digest `sha256:e09b6a0071813c68677c98f2608ab77c614a99a73582bcbeea2e828a4d091af9`。服务器一致性备份后迁移至schema12，AI标签回填与22旧候选加8新候选的自动补池实网通过。arm64仅构建检查，未原生执行。
