# 开发者预览运维

适用于 Linux 上单 authority、单服务进程的本机或受控私网部署。当前协议为 `task-core-draft-1`，HTTP binding 为 `ledger-http-draft-1`，SQLite 存储格式为 v1。运维操作不改变任务的 issuer、承接、交付或验收事实。程序退出、服务重启和会话结束均不代表任务完成。

## 持久目录与用户服务

将二进制、运行数据和测试材料分开保存，避免把日常实例留在 `/tmp`。建议布局：

```text
~/.local/lib/task-ledger/
  releases/<version>/taskledgerd
  current -> releases/<version>
~/.local/state/task-ledger/       # 0700
  server.json                   # 0600；使用相对 database/TLS 路径
  ledger.db                     # 0600；启动后可能有 -wal/-shm
  tls/                          # 私有证书材料；客户端只分发 CA 公钥
  clients/                      # 带外凭据、session proof、原请求意图
~/.local/state/task-ledger-backups/  # 0700；完整冷态快照，另存离机副本
```

配置格式与凭据生成规则见[后台接入](backend.md)。配置中的数据库和 TLS 路径相对 `server.json`；使用相对路径可以在完整目录迁移时保持含义。客户端配置中的 URL 和 CA 路径需要另外核对。不要把配置、数据库、原始 Bearer、session proof 或请求意图提交到 Git 或公开发行包。

首次安装使用已校验的发行包，保留该版本的校验和与构建标识。下列 `<version>` 和解包路径须替换为实际版本：

```sh
umask 077
mkdir -p "$HOME/.local/lib/task-ledger/releases/<version>"
mkdir -p "$HOME/.local/state" "$HOME/.local/state/task-ledger-backups"
install -m 0755 /path/to/unpacked/bin/taskledgerd "$HOME/.local/lib/task-ledger/releases/<version>/taskledgerd"
ln -s "releases/<version>" "$HOME/.local/lib/task-ledger/current"
```

`current` 已存在时不要覆盖或重复初始化，按后面的升级流程处理。在解包根目录使用配置生成器创建全新的私有运行目录：

```sh
python3 tools/init-config.py --dir "$HOME/.local/state/task-ledger" --authority personal --port 8090
"$HOME/.local/lib/task-ledger/current/taskledgerd" -config "$HOME/.local/state/task-ledger/server.json" -init
```

生成器要求目标目录不存在；它生成私有配置和 owner 客户端材料，不初始化数据库或启动服务。`-init` 只对全新 authority 执行一次。已有 authority 正常启动时不带 `-init`，也不重复生成配置。需要 LAN HTTPS 时，启动前按后台说明配置证书和监听地址。

仓库的 [task-ledger.service](../deploy/task-ledger.service) 是用户级 systemd 模板，读取上述 `current` 和持久配置路径。安装前确认没有旧临时服务、手动进程或其他主机在运行同一 authority；不要以 SQLite 允许并发打开为由运行两个服务实例。

```sh
mkdir -p "$HOME/.config/systemd/user"
install -m 0644 deploy/task-ledger.service "$HOME/.config/systemd/user/task-ledger.service"
systemd-analyze --user verify "$HOME/.config/systemd/user/task-ledger.service"
systemctl --user daemon-reload
systemctl --user enable --now task-ledger.service
systemctl --user show task-ledger.service -p ActiveState -p SubState -p MainPID -p ExecStart
journalctl --user -u task-ledger.service -n 50 --no-pager
```

模板设置私有 umask、失败重启和 15 秒停止等待；daemon 自身有最多 10 秒的 HTTP 优雅停止。用户服务默认随用户会话启动。若需要注销后或开机无人登录时持续运行，由管理员按主机策略为运行用户配置 lingering；启用 user unit 本身不证明无人登录启动已验证。

服务日志进入用户 journal；日志不应承载 Bearer、证明或任务正文。使用已有客户端凭据执行 `service.info`，核对 authority、协议和 binding，再打开 `/` 页面。`ActiveState=active` 或端口监听都不能代替这些检查。非 loopback 监听必须配置 TLS，并让客户端正常验证 CA 和目标主机名/IP；不要用忽略证书校验代替部署验收。

## 最新停机状态的备份与迁移

本预览支持的窄范围是：**完整保存最新停机状态，并在该状态之后没有任何权威写入的前提下，恢复到新目录或新主机继续运行。** 原实例始终保持停止，目的实例成为唯一 authority。只迁移数据文件不包含外部工作成果；客户端请求意图、session proof、交付资源和凭据要一起保存或单独明确备份。

操作步骤：

1. 暂停使用，记录当前二进制版本、配置路径和唯一运行实例。通过公开 API 保存 workspace/task 快照及关注的事件 cursor；凭据和快照含私有数据时只保存在私有证据目录。
2. 停止受管服务，确认退出成功并检查没有旧临时单元、手动进程或远端实例继续写同一 authority。若强制退出、状态未知或存在另一写入者，先处理该问题，不把本轮拷贝标为已验证冷备。
3. 完整复制运行目录，包括数据库、仍存在的 WAL/SHM、配置、证书/密钥及相关客户端材料。数据库或证书位于目录外时必须逐项纳入；禁止运行中仅复制 `ledger.db`，也禁止删除 WAL 后再复制。
4. 为静止副本记录 SHA-256 文件清单，并核对复制前后文件集合、摘要和私有权限。副本属于秘密资料，应另存受保护的离机备份。
5. 恢复到**不存在的新目录**，检查摘要。原实例保持停止；在目的端用相同兼容二进制启动，不运行 `-init`。更换监听地址/TLS 路径时保留配置变更记录，不改变 authority、数据库事实或凭据映射。
6. 通过公开 API 核对任务、报告、Submission/Decision、revision、会话绑定和事件水位；用保留的 cursor 查询，并同键读取一个已有成功回执。客户端恢复未知写仍使用原 ID、原请求和对应身份，不能生成新 ID 来“重做”。核对后再恢复业务写入。

使用标准目录的同机冷备示例（先确认第 2 步已满足）：

```sh
systemctl --user stop task-ledger.service
systemctl --user show task-ledger.service -p ActiveState -p SubState -p MainPID
# 预期 inactive/dead、MainPID=0；还须排除其他启动方式。
umask 077
ledger_snapshot="$HOME/.local/state/task-ledger-backups/$(date -u +%Y%m%dT%H%M%SZ)"
mkdir "$ledger_snapshot"
cp -a "$HOME/.local/state/task-ledger/." "$ledger_snapshot/"
(cd "$ledger_snapshot" && find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS)
(cd "$ledger_snapshot" && sha256sum --check SHA256SUMS)
```

此示例只校验快照文件可读取；完整迁移还需要第 4–6 步的源/目标集合比较和公开事实核对。运行目录内使用普通文件，避免符号链接使外部文件被遗漏。校验清单放在副本内，恢复时它只是运维资料，不是服务配置。

备份后若原实例继续产生了任何权威写入，该副本就不能再次成为相同 authority 的运行源。**不支持任意旧备份回滚、时间点恢复或双活。** 旧副本可能丢失已对外确认的幂等回执、任务版本和事件，令外部操作被重复执行。发生磁盘损坏、最新状态丢失或来源不明时，停止接单并保留证据；不要清库、清回执、修改 cursor 或偷偷换回旧文件来恢复“可用”。

## 升级与回退

升级前读取目标版本的协议、binding 和存储兼容说明，先在隔离环境运行发行包 smoke 和恢复演练。当前没有通用存储迁移/降级承诺；未知 schema 版本会被明确拒绝，见[SQLite 格式](../migrations/README.md)。

随后停止唯一实例，保存最新冷态状态，再把新二进制安装到独立版本目录，切换 `current` 并启动。记录旧/新版本及实际核对结果。不要原地覆盖仍在运行的二进制，也不要自动重放管理配置或重新初始化。

如果新版本无法启动，只有在确认**存储格式和业务事实仍与旧二进制兼容**时才能切回旧二进制；保留同一份最新数据库。不能用“二进制回退”暗中把数据也换回旧快照。目标版本已经写入不兼容状态时，应停止并按该版本专门的恢复方案处理。

## 凭据、容量与日常检查

凭据目录在启动时加载。轮换时保留 ActorRef，生成新的随机 Bearer/摘要，更新配置并重启；按当前权限处理历史回执。参与者登记不签发凭据，修改 `operators` 配置也不会修改已有数据库中的管理者名册。分发给客户端的只应是其自己的凭据。

浏览器 sessionStorage 不提供跨标签页关闭后的持久恢复；长任务应保留 Skill 的私有请求意图和 session proof。session 结束或进程失联不会自动释放任务，恢复时先查公开事实再采取合法动作。

事件和成功回执没有自动 GC。定期检查磁盘余量、服务退出/重启、错误、快照大小及实际延迟。当前 workspace 对象上限为 1000，完整响应上限 16 MiB；这些是明确拒绝的边界，不是已证明的性能容量。不要通过手工删历史或回执腾空间。

## 可复现的恢复演练

```sh
go build -o /absolute/private/bin/taskledgerd ./cmd/taskledgerd
python3 scripts/recovery_smoke.py \
  --daemon /absolute/private/bin/taskledgerd \
  --dir /absolute/new/private/recovery-evidence
```

`--dir` 必须是不存在的绝对目录，父目录须已存在；脚本拒绝覆盖。仅使用 Python 3 标准库和给定二进制，在随机 loopback 端口上操作自己创建的合成 authority，不读取现有服务配置，不调用 Agent 或模型。

脚本通过公开 HTTP 建立一项已完成任务和一项执行中任务，保存原请求；优雅停止源服务后复制完整目录并校验，再从新目录启动同一二进制。检查完整快照/水位、会话绑定、历史事件页和旧 claim 回执，继续提交与验收执行中任务，并从原 follow cursor 读取新增事实。最后停止所有自建进程，核对原目录和备份没有改变。

子进程启动前后及退出时原子更新私有 `resources.json`，记录 init/server 的 PID、启动时间、Linux `/proc` starttime、argv、归属和清理状态。SIGINT/SIGTERM 会清理当前直接子进程；记录中的历史 PID 仅供核对，不能不检查进程身份就据此 kill。

结果写入私有目录中的 `recovery-results.json`，只有 `passed: true` 且进程清理成功才通过。原请求、凭据、证明、数据库和日志都留在该目录供故障核查，不进入 Git。自动 `human` 主体的决定属于合成夹具，不代表真人已经批准产品。该演练验证同版本、同机、最新完整冷态迁移，不证明损坏介质恢复、旧状态回滚、跨版本升级、异机持久化保证或长时间稳定运行。
