# 可选 launcher 入口预留

未来可在拥有目标 Agent CLI 的主机运行受控 launcher，读取服务中已提交的 WakeRequest，按管理者登记的 LaunchProfile 启动 Agent。

目前没有可执行程序，不启动任何进程。服务中的任务事实、启动进程的事实、目标 Agent 的 claim 是独立事件；不能用成功启动代替后两者。重复和未知结果需核对，任务或注册资料不得成为 shell 脚本。

launcher 可与服务同机运行，也可作为受控工作站适配器；具体部署和调用契约待定，参见 [Agent 接入设计](../../docs/agent-integration.md)。
