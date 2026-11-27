package source

// 2026-11-27（F11 / 审计 N12）：解析旁证的**接线**。
//
// careapply 只认 SourceStatus.ParseFailed；这个字段必须由 refresh 从「本轮
// 到底解析出几条」推出来，否则守卫形同虚设。本用例跑真实刷新链路（本地
// httptest，不出网）把接线钉住：
//   · 正常索引 → Count>0、ParseFailed=false；
//   · 空索引（V2 空 apps）→ 当前解析器直接报错 → 走**失败通道**（Error 非空），
//     绝不会伪装成「成功但 0 应用」，因此 ParseFailed 保持 false。
// 第二条同时是不变式证明：0 条 + 无错在今天不可达，ParseFailed 是防将来解析器
// 放宽 / 上游换格式的兜底。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"moo/internal/config"
)

func TestRefreshParsedFlagPlumbing(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "moo.json") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"a":{"version":"1.0","download_url":"https://example.com/a.fpk"}}`))
	}))
	defer ok.Close()

	m := NewManager(&config.Config{Sources: []config.SourceRef{{Name: "t", URL: ok.URL}}})
	sts := m.RefreshAllConcurrent(1)
	if len(sts) != 1 {
		t.Fatalf("应有一轮状态, got %+v", sts)
	}
	if sts[0].Error != "" || sts[0].Count != 1 || sts[0].ParseFailed {
		t.Fatalf("正常源应 Count=1 / 无错 / ParseFailed=false, got %+v", sts[0])
	}

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "moo.json") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":2,"apps":{}}`))
	}))
	defer empty.Close()

	m2 := NewManager(&config.Config{Sources: []config.SourceRef{{Name: "e", URL: empty.URL}}})
	sts2 := m2.RefreshAllConcurrent(1)
	if len(sts2) != 1 || sts2[0].Error == "" {
		t.Fatalf("空索引必须报错（不得当成成功 0 应用）: %+v", sts2)
	}
	if sts2[0].ParseFailed {
		t.Errorf("失败轮本就不进空源通道，ParseFailed 应为 false: %+v", sts2[0])
	}
	if sts2[0].Count != 0 {
		t.Errorf("失败轮不得写入缓存条目, got %d", sts2[0].Count)
	}
}
