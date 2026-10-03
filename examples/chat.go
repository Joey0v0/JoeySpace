package examples

import _ "embed"

// ChatHTML 是 Gateway 提供的聊天演示页，构建时与脚本一同嵌入程序。
//
//go:embed chat.html
var ChatHTML string
