package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestUIInactiveGate 0.6.312 B3/F6 活性门控判定：
// 启动 40min 无请求 = 不活跃；请求后立即恢复活跃；启动 10min 内算活跃
// （冷启动首轮预热不被门控）；StartedAt 零值（单测构造）算活跃。
func TestUIInactiveGate(t *testing.T) {
	s := &Server{StartedAt: time.Now().Add(-40 * time.Minute)}
	if !s.uiInactive() {
		t.Error("启动 40min 且无 UI 请求应判定不活跃")
	}
	s.touchUI()
	if s.uiInactive() {
		t.Error("UI 请求后应立即恢复活跃")
	}

	s2 := &Server{StartedAt: time.Now().Add(-10 * time.Minute)}
	if s2.uiInactive() {
		t.Error("启动 10min 内应算活跃（首轮预热保障）")
	}

	s3 := &Server{}
	if s3.uiInactive() {
		t.Error("StartedAt 零值应算活跃")
	}

	// 边界：恰好 30min 内活跃、超过则不活跃
	s4 := &Server{StartedAt: time.Now().Add(-uiInactiveGate + time.Minute)}
	if s4.uiInactive() {
		t.Error("29min 无访问仍算活跃")
	}
	s5 := &Server{StartedAt: time.Now().Add(-uiInactiveGate - time.Minute)}
	if !s5.uiInactive() {
		t.Error("31min 无访问应不活跃")
	}
}

// TestUITouch_Middleware F6 接线验证：经 Handler 最外层 withUITouch 中间件
// 的真实 HTTP 请求即触活（两通道均在 Handler(trust) 末端包了该中间件）；
// pprof 走独立 listener（http.Serve(ln, nil)，不经过 Handler），结构上
// 不可能触活——此处验证中间件本身的触活语义与恢复路径。
func TestUITouch_Middleware(t *testing.T) {
	s := &Server{StartedAt: time.Now().Add(-40 * time.Minute)}
	if !s.uiInactive() {
		t.Fatal("前提：40min 无访问应不活跃")
	}
	h := withUITouch(s, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/apps", nil))
	if s.uiInactive() {
		t.Error("经最外层中间件的请求后应立即触活（恢复路径）")
	}
	// 无请求不触活：时间推进后仍不活跃（对照：只有中间件能改 lastUIRequest）
	s2 := &Server{StartedAt: time.Now().Add(-40 * time.Minute)}
	h2 := withUITouch(s2, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	_ = h2
	if !s2.uiInactive() {
		t.Error("未发请求的 Server 应保持不活跃")
	}
}
