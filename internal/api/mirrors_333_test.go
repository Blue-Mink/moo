package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"moo/internal/config"
)

// ── 0.6.333 F1：ghProbeURL 拼接矩阵 ────────────────────────────────────────
// 旧实现给 direct 塞了 raw.githubusercontent.com 前缀，拼出
// https://raw.githubusercontent.com/github.com/... 的非法地址 → 恒 404。
func TestGhProbeURL(t *testing.T) {
	asset := "github.com/Blue-Mink/moo/releases/download/v0.6.325/moo_0.6.325_x86.fpk"
	cases := []struct {
		name  string
		opt   config.MirrorOption
		asset string
		want  string
	}{
		{
			name:  "direct 补协议头（release 资产）",
			opt:   config.MirrorOption{Key: "direct", Label: "直连 GitHub"},
			asset: asset,
			want:  "https://" + asset,
		},
		{
			name:  "普通镜像前缀拼接",
			opt:   config.MirrorOption{Key: "gh-proxy-hk", URL: "https://hk.gh-proxy.org/"},
			asset: asset,
			want:  "https://hk.gh-proxy.org/" + asset,
		},
		{
			name:  "空 URL 非 direct 不 panic（结果=资产原样）",
			opt:   config.MirrorOption{Key: "custom"},
			asset: asset,
			want:  asset,
		},
		{
			name:  "资产已是绝对 URL → 原样透传",
			opt:   config.MirrorOption{Key: "gh-proxy", URL: "https://gh-proxy.com/"},
			asset: "https://raw.githubusercontent.com/o/r/main/x.fpk",
			want:  "https://raw.githubusercontent.com/o/r/main/x.fpk",
		},
		{
			name:  "direct 与绝对资产 → 不重复加协议头",
			opt:   config.MirrorOption{Key: "direct"},
			asset: "https://example.com/x.fpk",
			want:  "https://example.com/x.fpk",
		},
	}
	for _, c := range cases {
		if got := ghProbeURL(c.opt, c.asset); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
	// 关键回归：direct 结果里不得出现 raw.githubusercontent.com
	if got := ghProbeURL(config.MirrorOption{Key: "direct"}, asset); strings.Contains(got, "raw.githubusercontent.com") {
		t.Errorf("direct 探针不应再拼 raw 域名: %q", got)
	}
}

// withGhAssets 临时替换探测资产列表（测试结束复原）。
func withGhAssets(t *testing.T, assets []string) {
	t.Helper()
	old := ghProbeAssets
	ghProbeAssets = assets
	t.Cleanup(func() { ghProbeAssets = old })
}

// ── 0.6.333 F2：429 → limited（httptest 伪造） ─────────────────────────────
func TestProbeGhOne429IsLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	withGhAssets(t, []string{"github.com/o/r/releases/download/v1/x.fpk"})

	m := NewMirrorMonitor()
	st := m.probeGhOne(context.Background(), config.MirrorOption{Key: "mirror-x", Label: "M", URL: srv.URL + "/"})
	if st.Status != "limited" {
		t.Fatalf("429 应判 limited, got %q (%s)", st.Status, st.Error)
	}
	if !strings.Contains(st.Error, "限流") {
		t.Errorf("限流原因短语缺失: %q", st.Error)
	}
	if st.ConsecFails != 0 {
		t.Errorf("探测本身不写计数, got %d", st.ConsecFails)
	}
}

// 403 仍是真实故障（与 429 区分）。
func TestProbeGhOne403IsFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	withGhAssets(t, []string{"github.com/o/r/releases/download/v1/x.fpk"})
	m := NewMirrorMonitor()
	st := m.probeGhOne(context.Background(), config.MirrorOption{Key: "k", URL: srv.URL + "/"})
	if st.Status != "fail" || !strings.Contains(st.Error, "403") {
		t.Fatalf("403 应判 fail, got %q / %q", st.Status, st.Error)
	}
}

// F3：首位资产 404（下线）→ 自动顺延试下一个（自有资产挂了也不整体误判）。
func TestProbeGhOneAsset404FallsThrough(t *testing.T) {
	body := strings.Repeat("x", 600*1024) // > ghProbeMinBytes(512KB)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "gone") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	withGhAssets(t, []string{"github.com/o/r/releases/download/v1/gone.fpk", "github.com/o/r/releases/download/v1/ok.fpk"})

	m := NewMirrorMonitor()
	st := m.probeGhOne(context.Background(), config.MirrorOption{Key: "k", URL: srv.URL + "/"})
	if st.Status != "ok" {
		t.Fatalf("首个资产 404 应顺延到第二个并判 ok, got %q (%s)", st.Status, st.Error)
	}
	if st.SpeedBPS <= 0 {
		t.Errorf("应测出吞吐, got %d", st.SpeedBPS)
	}
}

// direct 探针走 scheme 拼接（不再 404）：同一 httptest 上模拟 direct 形态时
// 只能验证「URL 可被解析」——实际网络请求指向 https://github.com/...，
// 故此处只断言请求 URL 形态（ghProbeURL 已覆盖），避免测试依赖外网。
func TestProbeGhOneDirectURLShape(t *testing.T) {
	got := ghProbeURL(config.MirrorOption{Key: "direct"}, ghProbeAssets[0])
	if !strings.HasPrefix(got, "https://github.com/") {
		t.Fatalf("direct 探针地址形态错误: %q", got)
	}
	// 资产列表首位 = 自有稳定文件（F3）
	if !strings.Contains(ghProbeAssets[0], "Blue-Mink/moo") {
		t.Errorf("探测资产首位应为自有 release, got %q", ghProbeAssets[0])
	}
}

// ── 0.6.333 F2：limited 冻结连败计数 ──────────────────────────────────────
func TestBumpGhFailLimitedFreezes(t *testing.T) {
	m := NewMirrorMonitor()
	m.ghFails["k"] = 2

	st := MirrorStat{Key: "k", Status: "limited"}
	m.bumpGhFail(&st)
	if m.ghFails["k"] != 2 || st.ConsecFails != 2 {
		t.Fatalf("limited 应冻结计数(2), got fails=%d consec=%d", m.ghFails["k"], st.ConsecFails)
	}

	st = MirrorStat{Key: "k", Status: "fail"}
	m.bumpGhFail(&st)
	if m.ghFails["k"] != 3 || st.ConsecFails != 3 {
		t.Fatalf("fail 应累加到 3, got fails=%d consec=%d", m.ghFails["k"], st.ConsecFails)
	}

	st = MirrorStat{Key: "k", Status: "ok"}
	m.bumpGhFail(&st)
	if m.ghFails["k"] != 0 || st.ConsecFails != 0 {
		t.Fatalf("ok 应清零, got fails=%d consec=%d", m.ghFails["k"], st.ConsecFails)
	}
}

// ── 0.6.333 F2：优选排名不收 limited ──────────────────────────────────────
func TestTopSpeedKeysExcludesLimited(t *testing.T) {
	keys := topSpeedKeys([]MirrorStat{
		{Key: "a", Status: "ok", SpeedBPS: 100},
		{Key: "b", Status: "limited", SpeedBPS: 9_000_000, LatencyMS: 1},
		{Key: "c", Status: "fail"},
	})
	if len(keys) != 1 || keys[0] != "a" {
		t.Fatalf("只应返回 ok 源, got %v", keys)
	}
	if k := bestKey([]MirrorStat{{Key: "b", Status: "limited", SpeedBPS: 9_000_000}}); k != "" {
		t.Fatalf("全 limited 时 bestKey 应为空, got %q", k)
	}
}

// ── 0.6.333 F2：全组限流不算「全部加速源不可用」 ──────────────────────────
func TestMirrorsAllFailedLimitedIsReachable(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	mirrorAllFailedState.mu.Lock()
	mirrorAllFailedState.at = map[string]time.Time{}
	mirrorAllFailedState.down = map[string]bool{}
	mirrorAllFailedState.mu.Unlock()

	s := &Server{Cfg: &config.Config{}, Mirrors: NewMirrorMonitor()}
	setGhStats(t, s.Mirrors, []MirrorStat{
		{Key: "a", Status: "limited", Error: "429 限流（临时）"},
		{Key: "b", Status: "fail"},
	})
	s.mirrorsAllFailedCheck()
	for _, e := range s.Cfg.NotifyLog {
		if e.Event == "mirrors_all_failed" {
			t.Fatalf("全 limited（链路可达）不应推全挂告警: %+v", e)
		}
	}
	// 全 fail 仍应告警（真故障不放过）
	setGhStats(t, s.Mirrors, []MirrorStat{
		{Key: "a", Status: "fail"},
		{Key: "b", Status: "fail"},
	})
	s.mirrorsAllFailedCheck()
	n := 0
	for _, e := range s.Cfg.NotifyLog {
		if e.Event == "mirrors_all_failed" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("全 fail 应推 1 条, got %d", n)
	}
}

// ── 0.6.333 F2：手选源被限流 → 保持手选、不判失效 ────────────────────────
func TestEvaluateMirrorKeepsLimitedManual(t *testing.T) {
	m := NewMirrorMonitor()
	setGhStats(t, m, []MirrorStat{
		{Key: "a", Status: "ok", SpeedBPS: 100},
		{Key: "b", Status: "limited", ConsecFails: 2},
	})
	s := &Server{Mirrors: m, Cfg: &config.Config{Mirror: "b"}}
	active, reason := s.evaluateMirror("b", "gh", m.gh())
	if active != "b" {
		t.Fatalf("限流的手选源应保持生效, got %q", active)
	}
	if strings.Contains(reason, "失效") {
		t.Fatalf("限流不应给出「已失效」原因: %q", reason)
	}
	// 链路序列里手选源仍排首位
	keys := s.ghOrderedKeys()
	if len(keys) == 0 || keys[0] != "b" {
		t.Fatalf("手选限流源应仍在首位, got %v", keys)
	}
	// 真失败才切走
	setGhStats(t, m, []MirrorStat{
		{Key: "a", Status: "ok", SpeedBPS: 100},
		{Key: "b", Status: "fail", ConsecFails: 1},
	})
	active, reason = s.evaluateMirror("b", "gh", m.gh())
	if active != "a" || !strings.Contains(reason, "失效") {
		t.Fatalf("fail 应切到 a 并给失效原因, got %q / %q", active, reason)
	}
}
