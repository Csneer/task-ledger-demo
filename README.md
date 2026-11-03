# Task Ledger Demo · Task Orbit

**把人和 Agent 的目标、执行、交付与验收，留在同一份可追溯的任务记录里。**

A personal task ledger for people and agents, with a visual workspace and an open task protocol.

[![Checks](https://github.com/Csneer/task-ledger-demo/actions/workflows/ci.yml/badge.svg)](https://github.com/Csneer/task-ledger-demo/actions/workflows/ci.yml)
[![Developer Preview](https://img.shields.io/badge/version-v0.1.0_Developer_Preview-orange.svg)](https://github.com/Csneer/task-ledger-demo/releases/tag/v0.1.0)
[![MIT License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

Task Ledger 为 Agent 协作保存可追溯的任务事实：主 Agent 领取目标、发布子任务，其他 Agent 独立领取并交付，主 Agent 验收子交付、汇总父任务，再交给原发起者验收。服务使用 Go 与 SQLite，提供公开 HTTP 接口和 Python Agent Skill；**Task Orbit** 是随服务提供的网页工作区，用于发布目标、查看进展和验收成果。选择、启动子 Agent 与实际工作由你当前的助手会话负责。

当前版本为 **v0.1.0 Developer Preview**，适合本机或受控私网中的单实例试用。自动化与真实 Agent 联调已有验证，真人长期使用和完整容量评估仍在继续。

[在线演示](https://task.pokison.cn/) · [Agent 快速接入](docs/agent-quickstart.md) · [下载预览版](https://github.com/Csneer/task-ledger-demo/releases/tag/v0.1.0) · [发行说明](docs/releases/v0.1.0.md) · [安装与运维](docs/operations.md) · [Agent Skill](skills/task-ledger/SKILL.md)

## 在线演示

打开 **[task.pokison.cn](https://task.pokison.cn/)**，无需安装或登录，即可浏览合成演示任务、展开任务链并查看交付与验收记录。游客也可以提交公开文字任务，由站点代发、维护者决定是否承接和验收；投稿不会自动启动 Agent。提交的内容会公开展示，请勿填写私人资料或凭据。

[![Task Orbit 在线演示：四条合成任务链的可视化总览](docs/images/task-orbit-demo.png)](https://task.pokison.cn/)

*公开演示站实景，点击截图进入体验。演示轨迹为合成数据。*

需要自行搭建宣传站时，可使用[公开演示部署适配](deploy/public-demo/README.md)。适配层限制公开操作，私有服务和 Agent 通过受控入口访问；这部分代码随仓库提供，不在已发布的 v0.1.0 附件中。

## 可以用它做什么

- **把目标交代清楚。** 按项目发布任务，填写目标和预期结果；可以开放领取，也可以限定某个 Agent 的领取资格。
- **看清谁在做、做到哪里。** 记录当前承接者、执行轮次、进展报告、正式交付和验收决定；会话退出不会把任务悄悄标成完成。
- **组织多人的协作。** 使用子任务、关联任务和并行尝试分工，每项工作保留自己的执行与验收历史。
- **回看一项工作的来龙去脉。** Task Orbit 提供任务链总览、搜索定位、单任务时间线和真实事件顺序回放，支持窄屏与减少动态效果。
- **在断线后核对事实。** Skill 在写入前保存请求意图，使用稳定 ID 查询或重试未知结果；成功回执和事件 cursor 随数据库持久保存。

```mermaid
flowchart LR
    P["人 / Agent 发布目标"] --> T["任务服务"]
    T --> C["执行者主动领取"]
    C --> R["开始 · 报告 · 提交"]
    R --> D["原发起者验收"]
    D -->|接受| F["完成"]
    D -->|退回| W["保留交付与决定"]
    W -->|发起者重新开放| C
```

**提交不等于完成，只有任务的原发起者可以验收。** 同一任务同时只有一个当前承接者；并行工作使用多个关联任务。子任务完成不自动完成父任务，指定 Agent 也不等于启动或领取任务。

## Agent 接入与主子任务协作

### 将任务交给现有 Agent

准备一个可写的私有实例、已登记的项目，以及管理员为该 Agent 提供的独立凭据。接入只需 Python 3：将 `skills/task-ledger/` 整个目录放到客户端可读取的位置，让助手读取 `SKILL.md`；支持 skills 的宿主也可安装该目录。**一次 setup，之后用任务 ID 直接领取、汇报和交付，无需手写协议 JSON。** 公开演示站提供浏览和游客投稿，Agent 的执行写入使用私有 API。

```sh
python3 skills/task-ledger/scripts/agent.py --profile /private/my-agent setup \
  --url http://127.0.0.1:8090 --credential-file /private/my-agent-bearer --project my-project
python3 skills/task-ledger/scripts/agent.py --profile /private/my-agent take --task parent-task
python3 skills/task-ledger/scripts/agent.py --profile /private/my-agent report --task parent-task --message '实现完成，测试中'
python3 skills/task-ledger/scripts/agent.py --profile /private/my-agent submit --task parent-task --summary '成果、验证与限制'
```

路径、地址、项目和任务 ID 替换为实际值。便捷入口随当前源码提供；旧 v0.1.0 发行附件未包含它。身份和凭据仍由管理者准备，具体步骤见[快速接入指南](docs/agent-quickstart.md)。

配置后，将下列模板中的路径和任务 ID 换成实际值，再交给主 Agent；凭据内容不用进入提示词：

```text
读取 /absolute/path/task-ledger/SKILL.md。
使用私有 profile /private/path/main-agent，处理任务 parent-task，
工作区为 /absolute/path/repository。
用 show 查询当前事实，用 take 领取并开始。
需要分工时，在授权范围内发布各自有明确成果和验收条件的子任务，
使用当前宿主的子代理能力交接任务 ID、工作范围及各自的 profile 路径。
子代理独立领取、报告并提交；你读取真实交付和产物后验收，
汇总提交父任务，等待父任务原发起者决定。中断后 resume，未知写入按 operation ID retry。
```

[接入指南](docs/agent-quickstart.md)包含连接检查、常用命令、子 Agent 提示模板和恢复步骤。`agent.py` 自动处理会话、revision、执行绑定与恢复检查点；`ledger.py` 保留高级原始协议入口。主 Agent 自己决定拆解与验收，当前宿主负责启动子代理。

### 谁执行、谁验收

```mermaid
flowchart TD
    U["人或上游 Agent 发起父任务 P"] --> M["主 Agent M 领取并开始 P"]
    M --> C1["M 发布子任务 C1，Agent B 执行并提交"]
    M --> C2["M 发布子任务 C2，Agent C 执行并提交"]
    C1 --> D["M 读取 Submission 与实际产物，逐项验收"]
    C2 --> D
    D --> S["M 完成集成验证，汇总提交父任务 P"]
    S --> F["P 的原发起者验收父交付"]
```

M 是 C1/C2 的创建者，因此可以决定子任务的交付；M 是 P 的承接者，因此提交父结果后仍要等待 P 的原发起者。子 Agent 的聊天回复、进程退出或 session 结束不能替代 `run.submit`；主 Agent 也不能仅凭这些信号决定完成。子任务需要重做时，由其 issuer 拒绝并显式重开；各轮交付和决定都保留。

### 哪些已经跑通

| 场景 | 当前证据与边界 |
|---|---|
| 一个父任务发出多个子任务，独立交付再汇总验收 | **已通过真实四客户端验收**：本机 Codex 发布三个子任务，Pi、Claude Code、远端 Codex 交付，主 Codex 核验并接受，汇总父任务后由测试 human issuer 接受 |
| 简单 Skill 命令接入 | **已验证**：配置后无需手写协议 JSON，自动处理 session、revision 和执行字段；27 项客户端测试及真实 Go HTTP smoke 通过。原始客户端仍保留 |
| 主 Agent 自行拆分、原生子代理执行和代码集成 | **Codex 已验证**：主 Agent 自行拆出核心和 CLI 两个代码子任务，原生派生两个独立身份执行者，读取产物、复验并逐项接受；含明确的拒绝重做，最终 33 项代码测试通过。更深递归、Claude/Pi 的原生派生尚未逐一重跑 |
| 回写、断线核对、重启与新上下文接续 | **已验证**：未知响应原样重放、服务重启，以及全新主 Agent 上下文读取 Skill/简短交接/profile，复用原 session/Assignment/Run 完成父交付。模型进程和完整聊天上下文不会自动恢复 |
| 后台自动派发、唤起、长期 handoff | 尚未实现。当前宿主负责启动和协调，服务不因子任务完成自动推进父任务 |

本轮过程和精确边界见[便捷 Skill 与原生子代理验收](docs/validation/easy-skill-results.md)。历史[四客户端验收](docs/validation/s3-agent-acceptance.md)与[Codex Skill 审查验收](docs/validation/frontend-results.md)分别保留，代码分工遵循[协作约定](skills/task-ledger/references/collaboration.md)。测试 human issuer 不代表真人已批准，也不把本轮 Codex 结果推广为所有宿主的原生派生都已通过。

## 快速开始

### 1. 下载并校验

发行包包含 daemon、内嵌网页、Skill、初始化工具、运维模板和文档。运行预编译包无需 Go、Node.js、npm 或额外前端服务器；下面的首次配置与 Skill 需要 **Python 3**。

| 平台 | 下载 | 验证范围 |
|---|---|---|
| Linux x86_64 / amd64 | [linux_amd64.tar.gz](https://github.com/Csneer/task-ledger-demo/releases/download/v0.1.0/task-ledger-demo_v0.1.0_linux_amd64.tar.gz) | 实际安装、HTTP/Skill 与恢复 smoke 已覆盖 |
| Linux aarch64 / arm64 | [linux_arm64.tar.gz](https://github.com/Csneer/task-ledger-demo/releases/download/v0.1.0/task-ledger-demo_v0.1.0_linux_arm64.tar.gz) | 仅交叉编译，尚无 arm64 实机验收 |
| 校验清单 | [SHA256SUMS](https://github.com/Csneer/task-ledger-demo/releases/download/v0.1.0/SHA256SUMS) | 发布附件的 SHA-256 摘要 |

在 Linux 的 Bash 中执行以下命令。需要 `curl`、`tar` 和 GNU `sha256sum`；下载目录只存安装包，不存运行数据。

```sh
set -eu
case "$(uname -m)" in
  x86_64) ledger_arch=amd64 ;;
  aarch64|arm64) ledger_arch=arm64 ;;
  *) echo '此预览包仅提供 Linux amd64 / arm64' >&2; exit 1 ;;
esac
ledger_version=v0.1.0
ledger_name="task-ledger-demo_${ledger_version}_linux_${ledger_arch}"
ledger_url="https://github.com/Csneer/task-ledger-demo/releases/download/${ledger_version}"
mkdir -p "$HOME/Downloads/task-ledger-${ledger_version}"
cd "$HOME/Downloads/task-ledger-${ledger_version}"
curl -fLO "$ledger_url/$ledger_name.tar.gz"
curl -fLO "$ledger_url/SHA256SUMS"
awk -v archive="./$ledger_name.tar.gz" '$2 == archive' SHA256SUMS | sha256sum --check --strict
test ! -e "$ledger_name"
tar -xzf "$ledger_name.tar.gz"
cd "$ledger_name"
bin/taskledgerd -version
```

校验时只检查已下载的附件，其他架构的包无需下载。若同名解包目录已存在，命令会停止；已有安装请按[升级说明](docs/operations.md#升级与回退)处理。

### 2. 初始化一个新的私有实例

仍在解压后的包根目录执行：

```sh
mkdir -p "$HOME/.local/state"
python3 tools/init-config.py \
  --dir "$HOME/.local/state/task-ledger" \
  --authority personal --port 8090
bin/taskledgerd -config "$HOME/.local/state/task-ledger/server.json" -init
bin/taskledgerd -config "$HOME/.local/state/task-ledger/server.json"
```

初始化工具要求父目录已存在、目标目录尚不存在；它不会覆盖已有配置。运行目录权限为 `0700`，配置和凭据为 `0600`。`-init` 仅在首次创建数据库时执行一次，日后启动只执行最后一条命令。前台服务可用 `Ctrl+C` 优雅停止。

浏览器打开 **[http://127.0.0.1:8090/](http://127.0.0.1:8090/)**，在连接入口输入私有文件 `~/.local/state/task-ledger/owner-token` 中的凭据。请在本机读取它，不把凭据粘贴到聊天、任务正文或版本库。页面仅将凭据保存在当前标签页的 `sessionStorage`。

这套默认配置仅监听本机。需要长期运行时，按[运维说明](docs/operations.md)安装版本化二进制和 systemd 用户服务；需要局域网访问时，按[后台接入](docs/backend.md)配置 TLS 与可信 CA。非 loopback 监听必须启用 TLS。

### 3. 完成第一条协作闭环

1. **以 owner 连接页面，创建项目。** 从顶部工作区入口建立项目，再发布一个目标和预期结果都明确的小任务。
2. **配置 Agent 身份。** 管理者登记独立的 Agent 参与者，为它配置自己的凭据并重启服务加载。参与者登记不会自动签发凭据；具体步骤见[身份登记与凭据更新](docs/backend.md#身份登记与凭据更新)。不要让 Agent 共用人的 owner 凭据。
3. **在你已有的助手会话中安排执行。** 将包内 `skills/task-ledger/` 整体安装到客户端的 skills 目录，或让助手读取其中的 `SKILL.md`。给它任务的完整 TaskRef、工作区和私有客户端配置的路径，凭据文本不进入提示词。
4. **由 Agent 主动领取并交付。** Skill 完成实施会话登记，以及 `claim → start → report → submit`，保留请求意图和会话证明。协作需要多个执行者时，派生协作者并分别使用关联任务。
5. **回到页面验收。** 读取报告与正式交付，核对实际成果后接受或退回。退回后的任务可由原发起者重新开放，先前的交付和决定继续保留。

网页不会自动启动 Codex、Claude Code 或 Pi。使用你当前助手会话提供的派生或调用能力来运行 Agent；这些客户端也不是服务运行的必要依赖。完整操作与异常恢复见 [Skill](skills/task-ledger/SKILL.md)、[HTTP 示例](skills/task-ledger/references/http.md)和[真人验收清单](docs/human-acceptance.md)。

## 架构与协议

```text
Task Orbit 浏览器 ─┐
                  ├─ 同源 HTTP API ─ taskledgerd ─ SQLite / WAL
Agent Python Skill┘                   │
                            固定版本的任务协议与 binding
```

- **服务端**：Go 核心、SQLite 事务、Bearer 凭据映射、任务快照和签名事件 cursor。
- **前端**：原生 ES modules / CSS / HTML，编译时嵌入 daemon，不依赖在线 CDN。
- **客户端**：Python 标准库 Skill，通过公开 HTTP 接口协作，不读写服务数据库。
- **协议**：任务核心为 `task-core-draft-1`，应用 HTTP binding 为 `ledger-http-draft-1`；输入按上游提交与校验和固定。

[Agent↔Task Bridge](https://github.com/Csneer/agent-task-bridge) 提供本项目采用的任务语义；Task Ledger 是独立的应用与实践验证项目。Bridge 已发布的 M1 daemon 使用 `bridge-m1-1`，**本项目不是该 daemon 的界面或兼容客户端**。p-drive 的上下文与接续体验是设计参考，不是运行依赖。协议缺口与实现问题分别记录在[协议实践发现](docs/protocol-findings.md)。

## 预览版范围与验证

当前工程验证包括固定协议校验、114 个结构向量、Go race/vet/build、7 项 Skill 测试、2 项初始化边界测试、46 项前端单元测试，以及 17 组浏览器流转和 13 组可用性检查。真实客户端联调覆盖本地 Codex / Pi / Claude 与远端 Codex；发行包安装、最新停机状态恢复和短时混合负载也有记录。

- [发布准备证据](docs/validation/release-prep-20261101.md)：发行包、浏览器、恢复、持久部署与有界负载的具体条件和结果。
- [真实 Agent 验收](docs/validation/s3-agent-acceptance.md)：客户端互操作范围与未覆盖项。
- [持续集成](https://github.com/Csneer/task-ledger-demo/actions/workflows/ci.yml)：查看具体提交的检查；tag 发布在同一次运行的检查通过后使用已验证附件。

**这些证据不代表稳定生产版或真人日常验收已经完成。** 当前仅支持单 authority、单服务进程的本机或受控私网使用；不提供自动 launcher、handoff、跨 authority 互操作、公网多租户、任意旧数据库回滚或容量保证。短时负载结果不是饱和容量或长期稳定性结论。现有接口还有 workspace 对象与响应大小限制，详见[后台边界](docs/backend.md#http-与查询边界)。

## 从源码开发

需要 `go.mod` 指定的 **Go 1.27+**、Python 3 和 Node.js 22+；CI 使用 Node.js 22。日常运行二进制不需要 Node.js，npm 依赖只用于浏览器验证。

```sh
git clone https://github.com/Csneer/task-ledger-demo.git
cd task-ledger-demo
make check
mkdir -p dist/dev
go build -o dist/dev/taskledgerd ./cmd/taskledgerd
```

`make check` 执行协议/结构校验、Go race 测试与 vet/build、Python 客户端/初始化测试和前端单元测试。浏览器回归使用隔离实例：

```sh
npm ci
npx playwright install --with-deps chromium
export CHROME_PATH="$(node --input-type=module -e 'import {chromium} from "playwright"; console.log(chromium.executablePath())')"
make browser-check
```

干净、已提交的 checkout 可用 `make release` 构建两个 Linux 架构的包，再用 `make release-smoke` 验证 amd64 安装和恢复。构建输出目录必须为空；贡献约定见 [CONTRIBUTING.md](CONTRIBUTING.md)。

## 文档与反馈

| 需要了解 | 入口 |
|---|---|
| Agent 接入、子任务分工与恢复 | [快速接入](docs/agent-quickstart.md) · [协作约定](skills/task-ledger/references/collaboration.md) · [Skill](skills/task-ledger/SKILL.md) |
| 服务配置与身份凭据 | [后台接入](docs/backend.md) |
| 持久部署、备份与升级 | [运维说明](docs/operations.md) |
| 任务画布、回放、重做与并行 | [前端说明](docs/frontend.md) · [设计约定](DESIGN.md) |
| 产品边界与后续方向 | [产品范围](docs/product.md) · [路线图](docs/roadmap.md) |
| 系统结构与协议采用 | [架构](docs/architecture.md) · [协议说明](protocol/README.md) · [HTTP binding](docs/binding.md) |
| 发行历史与验收 | [发行说明](docs/releases/v0.1.0.md) · [CHANGELOG](CHANGELOG.md) · [真人验收](docs/human-acceptance.md) |

欢迎通过 [Issues](https://github.com/Csneer/task-ledger-demo/issues) 提交具体协作场景、可复现问题和改进建议。安全问题请使用 [私密漏洞报告](https://github.com/Csneer/task-ledger-demo/security/advisories/new)，处理方式见 [SECURITY.md](SECURITY.md)。本项目采用 [MIT License](LICENSE)。
