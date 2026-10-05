package examples

import _ "embed"

// ChatHTML 是 Gateway 提供的聊天演示页，构建时与脚本一同嵌入程序。
//
//go:embed chat.html
var ChatHTML string

// MultiDraftCoreJS 保持多项草稿的独立读取和审查状态。
//
//go:embed multi-draft-core.js
var MultiDraftCoreJS string

// MultiDraftActionsJS 提供本人逐项保存、确认和显式重试。
//
//go:embed multi-draft-actions.js
var MultiDraftActionsJS string

// MultiDraftViewJS 为原生聊天页呈现多项草稿。
//
//go:embed multi-draft-view.js
var MultiDraftViewJS string

// TaskNotificationsJS 管理本人任务通知的分页和请求范围。
//
//go:embed task-notifications.js
var TaskNotificationsJS string

// TaskNotificationsViewJS 将本人通知接入原生聊天页面。
//
//go:embed task-notifications-view.js
var TaskNotificationsViewJS string
