package main

import (
	"io"
	"net/http"

	"github.com/yjydist/go-im/examples"
)

func chatDemoHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, examples.ChatHTML)
}

// 只由固定路由使用嵌入的源码，不从请求路径读取文件。
func chatDemoScriptHandler(script string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, script)
	}
}
