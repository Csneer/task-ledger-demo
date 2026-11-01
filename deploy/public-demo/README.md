# 公开演示与游客投稿

这组文件为 Task Ledger 提供一个受限的公开宣传入口：访客免登录浏览演示轨迹，也可以提交公开文字任务。维护者决定是否承接，再用自己的 Agent 执行并验收。它使用 **v0.1.0 原版 Go daemon**，没有改变任务协议；适配文件随源码提供，不包含在已经发布的 v0.1.0 附件中。

这不是开放注册的多租户服务。不要把已有私有 authority、数据库、任务或客户端材料复制到公开实例。页面能读到的全部数据都应适合公开，包括参与者、会话和任务历史。

## 访问路径与身份

```text
浏览器 → HTTPS / Cloudflare Tunnel → nginx :4174
                                    ├ 页面及只读 API → Go 127.0.0.1:4175
                                    └ POST /demo/tasks → intake 127.0.0.1:4176
                                                           └ 固定 task.create → Go
维护者 / 私有 Agent → SSH 转发 → Go 127.0.0.1:4175
```

- nginx 对读取请求固定注入只读 `human/visitor` 凭据，调用者提供的 Authorization 不能提升权限；原生 `/v1/commands`、`/v1/actions` 在公开入口返回 403。
- 浏览器中的 `public-demo` 只是让原版页面自动连接的占位字符串，不是真实凭据。真实 Bearer 留在服务器私有配置中。
- 投稿服务只接受 UUID、目标、预期结果和公开同意，不接受 Actor、项目、任意命令或附件。它以 `human/public-requests` 代发任务，固定放入 `guest-requests` 项目。
- **站点接待身份是任务的 issuer，由维护者使用该身份验收。** 访客没有被注册为独立参与者，管理员或 Agent 也不能代替 issuer 决定是否完成。
- 投稿不会自动运行 Agent。目前没有 Agent 自助注册；已有 Agent 通过私有入口主动领取、执行和交付。任务文字是公开外部输入，应由维护者判断是否执行。

## 文件与目录

| 文件或目录 | 用途 |
|---|---|
| `intake.py` | Python 标准库投稿服务，无额外 pip 依赖 |
| `bootstrap.js`、`style.css` | 自动只读连接与游客投稿界面 |
| `nginx.conf.example` | 独立 server 配置、路径白名单和速率限制 |
| `task-ledger-intake.service` | systemd 系统服务模板 |
| `test_intake.py` | 权限、输入、重放、容量和未知结果回归 |
| `/opt/task-ledger/current/bin/taskledgerd` | 指向已校验发行包的 daemon |
| `/etc/task-ledger/server.json` | Go 服务配置，私有 |
| `/var/lib/task-ledger/` | 独立公开 authority 的 SQLite 状态 |
| `/opt/task-ledger/public-demo/` | 本目录中的运行时适配文件 |
| `/opt/task-ledger/site/index.html` | 挂载适配脚本和样式的入口 |
| `/etc/task-ledger-public/` | intake 配置及 issuer 凭据，私有 |
| `/var/lib/task-ledger-public/intents/` | 原始投稿意图及成功回执，私有 |

Go 和 intake 使用不同的非登录账户：`taskledger`、`taskledger-public`。各自的配置及状态目录使用 `0700`，配置和凭据文件使用 `0600`，归对应服务账户所有；程序目录可读、daemon 可执行。nginx 含只读凭据的最终配置由 root 持有，权限为 `0600`。不要使用 `nginx -T` 将凭据配置打印到终端或日志。

## 安装顺序

先按[发行包安装与运维](../../docs/operations.md)核验下载校验和，检查端口、账户和目标目录是否已存在。下面是独立新实例的安装步骤，不应覆盖现有服务或重复执行 `-init`。

1. 将发行包安装到 `/opt/task-ledger/releases/<version>/`，用 `current` 链接指向它。建立独立配置和数据库，Go 监听 `127.0.0.1:4175`，示例 authority 为 `task-public-demo`。首次初始化后，以 `taskledger` 安装并启用 `task-ledger.service`；`ExecStart` 为 `/opt/task-ledger/current/bin/taskledgerd -config /etc/task-ledger/server.json`。
2. 依照[身份登记与凭据更新](../../docs/backend.md#身份登记与凭据更新)，通过 API 登记独立 viewer、站点接待和受控 Agent，并在服务配置中分配各自凭据。viewer 只有 read；接待和 Agent 有 read/write、不具备 admin。创建 `demo`、`guest-requests` 项目，用公开 API 写入明确标记为合成数据的演示任务。参与者登记本身不会签发凭据。
3. 将 `intake.py`、`bootstrap.js`、`style.css` 安装到 `/opt/task-ledger/public-demo/`。为 intake 单独保存接待凭据 `/etc/task-ledger-public/issuer-token`，创建如下配置：

   ```json
   {
     "listen_port": 4176,
     "upstream_url": "http://127.0.0.1:4175",
     "issuer_credential_file": "/etc/task-ledger-public/issuer-token",
     "authority": "task-public-demo",
     "project_id": "guest-requests",
     "allowed_origin": "https://task.pokison.cn",
     "intent_dir": "/var/lib/task-ledger-public/intents",
     "max_submissions": 100
   }
   ```

4. 将配置保存为 `/etc/task-ledger-public/config.json`，安装 intake 单元，再验证和启用：

   ```sh
   install -m 0644 task-ledger-intake.service /etc/systemd/system/task-ledger-intake.service
   systemd-analyze verify /etc/systemd/system/task-ledger-intake.service
   systemctl daemon-reload
   systemctl enable --now task-ledger-intake.service
   ```

5. 从本实例 `http://127.0.0.1:4175/` 保存原版 HTML 到 `/opt/task-ledger/site/index.html`，在原有 module 脚本之前加入以下两个外部资源。保留原有页面结构，避免内联脚本与 CSP 冲突：

   ```html
   <link rel="stylesheet" href="/demo.css">
   <script src="/demo-bootstrap.js"></script>
   ```

6. 将 nginx 模板放入此主机已有的 `conf.d` 加载目录，在主机内用 viewer 凭据替换 `VIEWER_TOKEN`，保持最终文件私有。不要把真实令牌放进命令参数、Git 或前端。执行 `nginx -t` 后 reload nginx，保留其他站点配置。
7. Tunnel 与 nginx 同机时，源站地址使用 **`http://127.0.0.1:4174`**；不同主机时，仅增加服务器实际 LAN 地址上的监听，再使用 **`http://<服务器LAN地址>:4174`**。例如当前部署使用 `172.16.100.100:4174`。这是 HTTP 源站，公网 TLS 由 Tunnel 提供；4175、4176 保持 loopback。跨主机连接应处于可信网络。
8. 模板仅信任本机 connector 的 `CF-Connecting-IP`。若 connector 在其他主机，应在核实其来源地址后增加精确 `set_real_ip_from`；不能信任任意地址，否则限速可被伪造。未配置远端来源时，同一 connector 的访客共享速率额度。

## 请求限制与恢复

提交格式如下，`id` 必须为规范 UUID：

```json
{"id":"d6d86f4e-0cec-4ddf-afd6-ab6a819f71c3","goal":"整理项目入门教程","expected_result":"提供一份可以照着完成首次任务的公开教程。","public":true}
```

目标为 4–200 字符，预期结果为 8–2000 字符，请求不超过 16 KiB。Origin 必须与配置完全匹配；Origin 检查不能识别人，也不能阻止脚本伪造来源，因此还配置了每 IP 每分钟 2 次投稿、突发 2 次，以及全实例投稿总量限制。读取默认每 IP 每秒 8 次、突发 30 次；每 IP 同时连接上限为 20。

默认总量是 **100 份已保存的投稿意图**，包括成功、明确失败和结果未知的请求，不只是活跃任务。旧 ID 在容量检查前处理，所以达到上限后仍可恢复旧投稿。维护者评估后可以调整 `max_submissions` 并重启 intake，目前代码允许 1–200；不要删除 intent、任务或回执来清空额度。

浏览器先在 localStorage 保存原正文与 UUID，服务端先持久化原命令再发给 Go。成功后公开任务 ID 为 `guest-<uuid>`。相同 ID、相同原始正文的重试返回同一任务；同 ID 改正文返回 409。断线或 5xx 不代表未写入，应使用页面的“核对原投稿”恢复，不要清除浏览器待核对记录、换 ID 再发或删除服务端意图。

`may_have_committed: true` 表示仍需核对。未知写入之后即使上游返回权限拒绝，也不能证明之前没有提交；维护者应修复连接或凭据，再恢复原请求。不要为得到成功状态放宽权限或绕过真实回执核对。

## 维护者与 Agent

当前实例的私有客户端配置保存在目标主机 `/root/task-ledger-admin/`：

- `owner-client.json`：管理身份和项目。
- `public-requests-client.json`：游客任务的发起者，负责取消、验收或重新开放。
- `pokison-agent-client.json`：为维护者保留的 Agent 身份，按 Skill 主动领取和交付。
- `visitor-client.json`：只读访问；不向浏览器分发真实令牌。

这些是路径说明，凭据及 session proof 不得进入任务正文、聊天或公开仓库。远端助手通过 SSH 转发私有 Go API：

```sh
ssh -N -L 127.0.0.1:14175:127.0.0.1:4175 root@172.16.100.100
```

确认本地端口空闲后使用转发。将**仅该 Agent 自己的**配置和凭据安全配置到客户端，私有配置中的 `service_url` 改为 `http://127.0.0.1:14175`；保留独立的 intent 和 session proof 目录。然后读取 [Task Ledger Skill](../../skills/task-ledger/SKILL.md)，查询 `guest-requests`，按 `claim → start → report → submit` 执行。`submit` 后仍等待站点接待身份验收。

## 运维与验收

```sh
systemctl is-active task-ledger task-ledger-intake nginx
systemctl is-enabled task-ledger task-ledger-intake
ss -ltn 'sport >= :4174 and sport <= :4176'
curl --fail http://127.0.0.1:4174/v1/identity
curl --fail https://task.pokison.cn/v1/identity
```

identity 应为 `visitor`，`read:true`、`write:false`、`admin:false`。源站正常而公网 502 时，核对 Tunnel 所在主机、源站协议和实际连接地址；不要改为公开 Go 的管理接口。只有真实域名返回预期页面和 JSON，才能认为公网链路通过。

变更后在桌面和手机检查自动连接、演示图、投稿说明、成功刷新和待核对恢复；使用一条明确标记为部署验证的任务测试，保留历史。确认任意 Authorization 都无法通过公开原生写入口。不要在真实站点批量制造任务测试配额；隔离测试已经覆盖并发上限和未知结果分支：

```sh
python3 -m unittest discover -s deploy/public-demo -p 'test_*.py' -v
node --check deploy/public-demo/bootstrap.js
```

重启 Go 时，其依赖的 intake 可能随之停止；维护操作完成后显式启动 intake，并核对两个单元及实际 HTTP。检查磁盘余量、错误日志和重启次数；nginx 使用 `/var/log/nginx/task-ledger.access.log`、`task-ledger.error.log`，应纳入主机已有的日志轮转。

备份前停止新投稿、排除私有写入者，先停止 intake 再停止 Go。完整保存最新 `/var/lib/task-ledger/`、`/var/lib/task-ledger-public/`、两个配置目录、nginx 私有配置及维护者/Agent 客户端材料，核对文件集合、SHA-256 和权限。服务恢复后启动 Go、启动 intake，再读 API 核对。

恢复必须保持唯一 authority 实例和全部最新事实，不执行 `-init`，不在已经接收新写入后换回旧数据库。保留 WAL/SHM、原请求意图和回执；不能运行中仅复制 `ledger.db`，也不能把“重试”改成新 ID。完整边界见[最新停机状态备份与迁移](../../docs/operations.md#最新停机状态的备份与迁移)。
