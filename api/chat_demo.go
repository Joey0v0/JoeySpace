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
