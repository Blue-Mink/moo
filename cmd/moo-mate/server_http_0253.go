package main

// 0.2.53（A3）：HTTP 服务器基础防护参数。
//
// 此前 TCP 与网关 unix socket 都直接用 http.ListenAndServe / http.Serve，
// 零超时、默认头大小 —— 慢头攻击或空闲连接可长期占用 fd/goroutine。
// 这里只加「读头超时 / 空闲超时 / 头大小」，**不设 WriteTimeout**：
// 软仓同步、自更新下载、日志/备份下载都是长响应，写超时会误杀。

import (
	"net/http"
	"time"
)

const (
	httpReadHeaderTimeout = 10 * time.Second
	httpIdleTimeout       = 120 * time.Second
	httpMaxHeaderBytes    = 64 << 10
)

// newHTTPServer 返回带基础防护参数的 Server；调用方自行设置 Addr 或 Serve(listener)。
func newHTTPServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: httpReadHeaderTimeout,
		IdleTimeout:       httpIdleTimeout,
		MaxHeaderBytes:    httpMaxHeaderBytes,
	}
}
