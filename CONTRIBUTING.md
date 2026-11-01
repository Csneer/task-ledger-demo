# 参与贡献

欢迎以真实人机协作场景、最小反例和可复现证据参与。当前版本为 v0.1.0 Developer Preview：Go 后台、公开 binding、Skill 与 Task Orbit 前端均已实现，干净源码构建、安装 smoke 和冷态恢复演练已通过。真人日常使用验收、长期运行与容量验证仍需持续积累；下一步计划见[路线图](docs/roadmap.md)。

修改前阅读[产品范围](docs/product.md)、[架构](docs/architecture.md)、[协议采用说明](protocol/README.md)和[验证计划](docs/validation.md)。讨论行为时标明：上游协议规则、本项目宿主约定、业务策略，或尚待决定的问题。

## 当前检查

准备 Go 1.27+、Node.js 22、Python 3、GNU make 和 sha256sum 后运行：

```sh
make check
```

此命令检查格式、固定协议摘要、binding 路由/操作/错误与 Schema 映射、正反结构向量和检查器自身测试，再执行 Go race/vet/build、Skill 测试和前端测试。它不代替真实 HTTP、浏览器、恢复或容量验证；结构向量的预期状态不能写成完整运行符合性。

浏览器回归另外按 [前端验证入口](docs/frontend.md#验证入口) 执行，需要 `npm ci` 和可用 Chromium/Chrome。不要对日常工作数据库运行合成播种或写入测试。

## 发布候选

发行包必须来自干净工作区。在 `make check` 通过并审查提交后运行：

```sh
bash tools/build-release.sh v0.1.0 dist/release
bash tools/smoke-release.sh dist/release v0.1.0
```

输出目录须为空。Linux arm64 包仅交叉编译；amd64 smoke 不能写成 arm64 实机验收。只有精确候选提交的检查通过、运行限制与真人验收清楚后，维护者才创建 tag/release。

## 实现与验证

每个实现切片对应一个公开验收场景；当前预览版的前端和 Skill 已有公开工程验证记录。先明确接口与预期事实，再通过服务入口验证成功、冲突与恢复；不要用内部数据库写入制造“成功路径”。

发现规格歧义时，按[问题记录模板](docs/evidence-template.md)保留最小输入、条款、预期和实际结果，再回馈上游。不要为方便 demo 私改协议枚举或完成语义。协议升级记录在[升级记录](protocol/UPGRADES.md)。

性能记录必须列明代码与协议版本、数据规模、硬件、并发、持久化设置、负载和指标口径。只提交脱敏或合成证据；本机数据库、凭据和原始日志放在忽略的 `runtime/`，不进入公共仓库。
