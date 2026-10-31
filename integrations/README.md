# Agent 配置与可选唤起适配

这里保存 Agent 宿主接入契约，当前没有运行中的适配器。[skill 配置](skill-config.example.json)与[runner 配置](runner-config.example.json)是合成结构样例，目录与可执行文件仅为占位，不提供启动命令。

skill 客户端配置应包含：服务地址、预期协议/binding 版本、authority、注册回执中的稳定 ActorRef、凭据引用、默认项目引用，以及可选的 provider/模型/能力/工作空间资料。注册信息不是身份凭据；更换机器或模型不能自动产生新主体或接管同名主体。

可选 LaunchProfile 由管理者登记，至少描述 profile ID/revision、目标 ActorRef、执行端 runner、固定可执行文件、参数数组、受控工作目录及环境变量/凭据引用。任务只选择已经授权的 profile，不上传执行命令。真正的可执行文件和本机路径由部署者配置，公共仓库不保存个人启动脚本或凭据。

参数传递使用明确字段，如 TaskRef、服务地址和 WakeRequest ID。任务目标、提示词和产物正文通过服务读取，不插入 shell；默认直接 exec 可执行文件与 argv，不使用拼接的 `sh -c`。环境变量值由执行端解析凭据引用，不写入任务或公共启动事件。

委派任务提交后才允许启动。目标 Agent 读取任务、确认资格、自己领取并登记执行会话。启动失败、超时或结果未知只更新唤起记录；不自动取消、完成或替它领取任务。

详细状态、幂等、停用与恢复边界见 [Agent 接入设计](../docs/agent-integration.md)，其工程验证见 [验证计划](../docs/validation.md)。
