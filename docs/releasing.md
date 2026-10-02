# 构建与完整发布

每次完整发布必须包含 Linux、Windows 二进制与 Docker 镜像，由 GitHub Actions 构建。

## 平台与产物

| 目标 | 架构 | 产物 |
| --- | --- | --- |
| Linux | amd64、arm64 | 两个 .tar.gz |
| Windows | amd64、arm64 | 两个 .zip，内含 .exe |
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

1. 复用 CI，全部测试、四个平台包和 Docker 验证成功后继续。
2. 使用 GITHUB_TOKEN 推送 GHCR 双架构镜像。
3. 下载并校验四个压缩包，创建或恢复 GitHub Release 草稿并上传全部资产。
4. 镜像和二进制均就绪后才发布 Release；说明中记录镜像 digest。
5. 带后缀的版本标记 prerelease；稳定版本标记最新并更新镜像 latest。

镜像路径：ghcr.io/untitled572/pixiv-curator。例如版本 v0.1.0 的镜像标签为 :v0.1.0，此处仅举例，并不表示已发布。

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

要求 Go 1.25+、Python 3.11+；Docker 验证需要 Buildx。

```sh
make check build
python3 scripts/build-release.py --version dev
(cd dist && sha256sum --check SHA256SUMS)
python3 scripts/smoke-test.py --binary ./bin/pixiv-curator --version dev
docker buildx build --load --platform linux/amd64 -t pixiv-curator:dev .
docker run --rm -p 127.0.0.1:5722:5722 pixiv-curator:dev
```

make check build 会检查代码并生成 bin/pixiv-curator。CI 不依赖本地 dist/bin。

Docker 使用非 root UID 65532、scratch 运行层，包含 CA 证书；镜像内监听 0.0.0.0:5722，主机示例仅绑定 127.0.0.1。当前镜像只运行初始化服务，未实现持久业务数据；SQLite 实现阶段补充数据卷与目录权限设计。

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
