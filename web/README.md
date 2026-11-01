# Task Orbit

任务星轨前端：原生 ES modules、SVG/Canvas、CSS，由 Go daemon 同源嵌入提供。视觉约定见 [DESIGN.md](../DESIGN.md)，使用与验收见[前端说明](../docs/frontend.md)。

`graph.js/css` 负责任务节点、关系布局、缩放和事件动效；`app.js/ui.css` 负责轻导航、详情和操作；`api.js/model.js` 对接公开协议、身份、会话和未知请求恢复。

`history.js` 使用独立事件游标载入历史轨迹。画布保留完成节点动效、编号与播放控制，也可从任务详情回看单个任务；历史回放只读，不改变任务当前状态。

前端使用公开接口，不直接读数据库，不修改核心状态字段。人工验收要发送由真实发起者授权的决定；页面上有“管理员”身份不能替代协议的 issuer。
