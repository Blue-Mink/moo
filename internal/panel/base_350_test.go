package panel

// 0.6.349（issue moo#7）WS2 全场景测试补强：ResolveBase 优先级与边界。
// 与 base_test.go 互补，不删旧；本文件用表驱动 + 覆盖 env 变化失效、
// MOO_PANEL_CONF 自定义路径、非回环回落、兜底 5666 等新增边界。

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// resetProbe 覆盖 probeDial 并确保测试结束还原（resetBase 不还原它）。
func resetProbe(t *testing.T) {
	t.Helper()
	old := probeDial
	t.Cleanup(func() { probeDial = old })
}

// writeConfAt 写自定义路径 conf（不改变 gatewayConf 包变量），返回路径。
func writeConfAt(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "custom.conf")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestResolveBasePriorityTable_350 表驱动：解析优先级链 + 边界。
func TestResolveBasePriorityTable_350(t *testing.T) {
	p1 := listenLocal(t)
	p2 := listenLocal(t)
	p3 := listenLocal(t)

	cases := []struct {
		name      string
		env       string
		explicit  string
		conf      string // 空串 = 不写 conf（走默认路径）
		failProbe bool
		want      string
		wantSrc   string
	}{
		{
			name:     "env 优先于 conf 与显式",
			env:      "http://127.0.0.1:" + strconv.Itoa(p1),
			explicit: "http://127.0.0.1:" + strconv.Itoa(p2),
			conf:     `{"schema":{"http":{"port":` + strconv.Itoa(p3) + `}}}`,
			want:     baseFromPort(p1, false),
			wantSrc:  baseEnvName,
		},
		{
			name:     "显式配置优先于 conf",
			explicit: "http://127.0.0.1:" + strconv.Itoa(p1),
			conf:     `{"schema":{"http":{"port":` + strconv.Itoa(p2) + `}}}`,
			want:     baseFromPort(p1, false),
			wantSrc:  "explicit",
		},
		{
			name:    "conf schema.http.port 生效",
			conf:    `{"schema":{"http":{"port":` + strconv.Itoa(p1) + `}}}`,
			want:    baseFromPort(p1, false),
			wantSrc: "gateway-conf",
		},
		{
			name:    "force_https 用 https 端口",
			conf:    `{"schema":{"http":{"port":5666},"https":{"port":` + strconv.Itoa(p1) + `}},"force_https":true}`,
			want:    baseFromPort(p1, true),
			wantSrc: "gateway-conf",
		},
		{
			name:      "conf 缺端口字段 → 跳过不 panic 兜底",
			conf:      `{"force_https":true}`,
			failProbe: true,
			want:      DefaultBaseURL,
			wantSrc:   "default",
		},
		{
			name:      "conf 坏 JSON → 跳过不 panic 兜底",
			conf:      `{not json`,
			failProbe: true,
			want:      DefaultBaseURL,
			wantSrc:   "default",
		},
		{
			name:    "非回环 env 192.0.2.1 → 拒绝回落 conf",
			env:     "http://192.0.2.1:5666",
			conf:    `{"schema":{"http":{"port":` + strconv.Itoa(p1) + `}}}`,
			want:    baseFromPort(p1, false),
			wantSrc: "gateway-conf",
		},
		{
			name:    "非回环 env example.com → 拒绝回落 conf",
			env:     "http://example.com:5666",
			conf:    `{"schema":{"http":{"port":` + strconv.Itoa(p1) + `}}}`,
			want:    baseFromPort(p1, false),
			wantSrc: "gateway-conf",
		},
		{
			name:     "非回环显式地址 → 拒绝回落 conf",
			explicit: "http://example.com:5666",
			conf:     `{"schema":{"http":{"port":` + strconv.Itoa(p1) + `}}}`,
			want:     baseFromPort(p1, false),
			wantSrc:  "gateway-conf",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resetBase(t)
			if c.conf != "" {
				writeConf(t, c.conf)
			}
			if c.failProbe {
				resetProbe(t)
				probeDial = func(string, time.Duration) bool { return false }
			}
			if c.env != "" {
				t.Setenv(baseEnvName, c.env)
			} else {
				t.Setenv(baseEnvName, "")
			}
			got := ResolveBaseCached(c.explicit)
			if got.URL != c.want || got.Source != c.wantSrc {
				t.Fatalf("got %+v, want url=%s src=%s", got, c.want, c.wantSrc)
			}
		})
	}
}

// TestResolveBaseConfEnvPath_350：MOO_PANEL_CONF 指向自定义路径生效。
func TestResolveBaseConfEnvPath_350(t *testing.T) {
	resetBase(t)
	p := listenLocal(t)
	custom := writeConfAt(t, `{"schema":{"http":{"port":`+strconv.Itoa(p)+`}}}`)
	t.Setenv(confEnvName, custom)
	// 同时把默认 gatewayConf 指向空 conf，证明走的是自定义路径
	writeConf(t, `{}`)
	got := ResolveBaseCached("")
	if got.URL != baseFromPort(p, false) || got.Source != "gateway-conf" {
		t.Fatalf("MOO_PANEL_CONF 自定义路径未生效: %+v", got)
	}
}

// TestResolveBaseFallbackDefault_350：conf 缺 + env 缺 + 探测失败 → 兜底 5666。
func TestResolveBaseFallbackDefault_350(t *testing.T) {
	resetBase(t)
	resetProbe(t)
	probeDial = func(string, time.Duration) bool { return false }
	writeConf(t, `{}`) // 无端口
	t.Setenv(baseEnvName, "")
	t.Setenv(confEnvName, "")
	got := ResolveBaseCached("")
	if got.URL != DefaultBaseURL || got.Source != "default" {
		t.Fatalf("兜底失败: %+v", got)
	}
}

// TestResolveBaseCacheEnvChange_350：env 值变化立即失效（无需 Invalidate）。
func TestResolveBaseCacheEnvChange_350(t *testing.T) {
	resetBase(t)
	p1 := listenLocal(t)
	p2 := listenLocal(t)
	t.Setenv(baseEnvName, "http://127.0.0.1:"+strconv.Itoa(p1))
	first := ResolveBaseCached("")
	if first.URL != baseFromPort(p1, false) {
		t.Fatalf("首解失败: %+v", first)
	}
	// env 变化 → 缓存失效，直接换到 p2
	t.Setenv(baseEnvName, "http://127.0.0.1:"+strconv.Itoa(p2))
	second := ResolveBaseCached("")
	if second.URL != baseFromPort(p2, false) {
		t.Fatalf("env 变化应使缓存失效: %+v", second)
	}
	// 清空 env → 再解析不应沿用 p2（应走 conf/probe/default）
	t.Setenv(baseEnvName, "")
	third := ResolveBaseCached("")
	if third.URL == baseFromPort(p2, false) {
		t.Fatalf("清空 env 后不应沿用缓存值: %+v", third)
	}
}
