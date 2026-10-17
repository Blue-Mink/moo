package api

// icon_alias_test.go —— 0.6.326d 图标「目录名 ≠ appname」别名回落单测。
// 数据形状取自 jsDelivr flat 的真实返回（33205q/FnDepot、KKKK987/FnDepot、
// 710850609/FnDepot、Brian099/fn_fpk_packages）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"moo/internal/config"

	"moo/internal/source"
)

// files33205q 33205q/FnDepot：6 个顶层目录，SmartDNS 大小写与 appname 不同。
var files33205q = []string{
	"/.github/workflows/update-repo-list.yml",
	"/.gitignore",
	"/fnpack.json",
	"/README.md",
	"/AdGuardHome/ICON.PNG",
	"/DBland/ICON.PNG",
	"/OpenList/ICON.PNG",
	"/SmartDNS/ICON.PNG",
	"/sources/fndepot.json",
	"/TaoSync/ICON.PNG",
}

// filesKKKK987 KKKK987/FnDepot：唯一顶层目录 xiansongshu（应用名「小松鼠」）。
var filesKKKK987 = []string{
	"/fnpack.json",
	"/README.md",
	"/xiansongshu/ICON.PNG",
	"/xiansongshu/README.md",
}

// filesMultiDir 710850609/FnDepot：8 个顶层目录（多目录仓库，规则 b 不适用）。
var filesMultiDir = []string{
	"/fnpack.json",
	"/AdGuardHome/ICON.PNG",
	"/DBland/ICON.PNG",
	"/EasyTier-EUI/ICON.PNG",
	"/fndepot.source/ICON.PNG",
	"/fndepot.source/fndepot.source/ICON.PNG",
	"/OpenList/ICON.PNG",
	"/SmartDNS/ICON.PNG",
	"/sources/fndepot.json",
	"/TaoSync/ICON.PNG",
}

func flatJSON(names ...string) string {
	type f struct {
		Name string `json:"name"`
	}
	var d struct {
		Files []f `json:"files"`
	}
	for _, n := range names {
		d.Files = append(d.Files, f{n})
	}
	b, _ := json.Marshal(d)
	return string(b)
}

// newFakeLister 起一个假的 jsDelivr 目录服务；返回 lister 与调用计数。
func newFakeLister(t *testing.T, h http.HandlerFunc) (*iconAliasLister, *int32) {
	t.Helper()
	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		h(w, r)
	}))
	t.Cleanup(ts.Close)
	l := newIconAliasLister(filepath.Join(t.TempDir(), "icon_alias.json"))
	l.apiRoot = ts.URL
	l.client = ts.Client()
	return l, &calls
}

// A. 归一化口径：小写 + 只留 a-z0-9；非 ASCII 归零（必须由规则 b 兜住）。
func TestIconAliasNorm(t *testing.T) {
	cases := map[string]string{
		"SmartDNS":       "smartdns",
		"smartdns":       "smartdns",
		"java-8-openjdk": "java8openjdk",
		"EasyTier-EUI":   "easytiereui",
		"fndepot.source": "fndepotsource",
		"小松鼠":            "",
		"":               "",
	}
	for in, want := range cases {
		if got := iconAliasNorm(in); got != want {
			t.Errorf("iconAliasNorm(%q) = %q, want %q", in, got, want)
		}
	}
}

// B. 规则 a（归一化相等）：smartdns → SmartDNS（多目录仓库，不得走规则 b）。
func TestIconAliasDirNormEqual(t *testing.T) {
	e := buildIconAliasEntry(files33205q)
	if got := aliasDir(e, "smartdns"); got != "SmartDNS" {
		t.Fatalf("aliasDir(smartdns) = %q, want SmartDNS", got)
	}
	if icons := e.Icons["SmartDNS"]; len(icons) != 1 || icons[0] != "SmartDNS/ICON.PNG" {
		t.Fatalf("SmartDNS 图标清单 = %v", icons)
	}
	// 顶层目录必须剔除隐藏项（.github）并按字典序
	if strings.Join(e.TopDirs, ",") != "AdGuardHome,DBland,OpenList,SmartDNS,TaoSync,sources" {
		t.Fatalf("TopDirs = %v", e.TopDirs)
	}
}

// C. 规则 b（唯一目录）：小松鼠 → xiansongshu（appname 归一化为空，规则 a 不参与）。
func TestIconAliasDirUniqueDirAlias(t *testing.T) {
	e := buildIconAliasEntry(filesKKKK987)
	if got := aliasDir(e, "小松鼠"); got != "xiansongshu" {
		t.Fatalf("aliasDir(小松鼠) = %q, want xiansongshu", got)
	}
}

// D. 多目录仓库 + 无归一化相等的 appname → 不误配（宁可不修，不给错图）。
func TestIconAliasDirNoFalseMatch(t *testing.T) {
	multi := buildIconAliasEntry(filesMultiDir)
	for _, name := range []string{"music-toolkit", "anniversary-reminder", "Nginx", "DDNS-GO", "lobechat", "小松鼠"} {
		if got := aliasDir(multi, name); got != "" {
			t.Errorf("多目录仓库 appname=%q 不应命中任何目录，实际 %q", name, got)
		}
	}
	// 空归一化值绝不能匹配任意目录（守卫：want == "" 不参与规则 a）
	odd := buildIconAliasEntry([]string{"/Foo/ICON.PNG", "/Bar/ICON.PNG"})
	if got := aliasDir(odd, "小松鼠"); got != "" {
		t.Errorf("归一化为空的 appname 不得匹配多目录仓库，实际 %q", got)
	}
	// 归一化同名但该目录无图标文件 → 不算命中
	noIcon := buildIconAliasEntry([]string{"/Foo/README.md", "/Bar/ICON.PNG"})
	if got := aliasDir(noIcon, "foo"); got != "" {
		t.Errorf("目录无图标文件不应命中，实际 %q", got)
	}
	// 规则 b 门槛：appname 含 ASCII 字母数字（归一化非空）时，即使仓库只有
	// 1 个目录也不猜（实测副作用：kalisgp-cmyk/FnDepot 只有 fntermx 一个目录，
	// 而 lobechat 的包在仓库根 → 猜了就是错图标）。
	single := buildIconAliasEntry([]string{"/fnpack.json", "/fntermx/ICON.PNG"})
	if got := aliasDir(single, "lobechat"); got != "" {
		t.Errorf("非纯非 ASCII appname 不得走唯一目录规则，实际 %q", got)
	}
	if got := aliasDir(single, "小松鼠"); got != "fntermx" {
		t.Errorf("纯中文 appname 应走唯一目录规则，实际 %q", got)
	}
}

// E. 源地址解析：仅 GitHub 系触发。
func TestIconAliasRepo(t *testing.T) {
	ok := []string{
		"https://github.com/33205q/FnDepot",
		"https://github.com/33205q/FnDepot.git",
		"https://github.com/KKKK987/FnDepot/raw/main/fnpack.json",
		"github.com/33205q/FnDepot",
	}
	for _, u := range ok {
		owner, repo, yes := iconAliasRepo(u)
		if !yes || owner != "33205q" && owner != "KKKK987" || repo == "" {
			t.Errorf("iconAliasRepo(%q) = (%q,%q,%v)，应识别为 GitHub 源", u, owner, repo, yes)
		}
	}
	bad := []string{
		"",
		"http://192.0.2.15:3033/bluemink/moo",
		"https://example.com/bluemink/FnDepot",
		"https://cdn.jsdelivr.net/gh/33205q/FnDepot@main/fnpack.json",
	}
	for _, u := range bad {
		if _, _, yes := iconAliasRepo(u); yes {
			t.Errorf("iconAliasRepo(%q) 不应识别为 GitHub 源", u)
		}
	}
}

// F. 候选 URL 生成：CJK 目录转义 + 分支前缀。
func TestIconAliasCandidates(t *testing.T) {
	got := aliasIconCandidates("KKKK987", "FnDepot", "main", "xiansongshu",
		[]string{"xiansongshu/ICON.PNG", "xiansongshu/ICON_256.PNG"})
	if len(got) != 2 || got[0] != "https://raw.githubusercontent.com/KKKK987/FnDepot/main/xiansongshu/ICON.PNG" {
		t.Fatalf("候选生成异常: %v", got)
	}
	cjk := aliasIconCandidates("o", "r", "main", "目录", []string{"目录/ICON.PNG"})
	if len(cjk) != 1 || !strings.Contains(cjk[0], "%E7%9B%AE%E5%BD%95/ICON.PNG") {
		t.Fatalf("非 ASCII 路径未转义: %v", cjk)
	}
}

// G. 列举缓存与失败退避：成功 72h 内不再请求；失败进负缓存不重试。
func TestIconAliasListerCacheAndFailure(t *testing.T) {
	l, calls := newFakeLister(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "@main/flat") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(flatJSON(files33205q...)))
	})
	e, ok := l.dirs(t.Context(), "33205q", "FnDepot")
	if !ok || e == nil || len(e.TopDirs) == 0 {
		t.Fatalf("首次列举应成功: ok=%v", ok)
	}
	if _, ok := l.dirs(t.Context(), "33205q", "FnDepot"); !ok {
		t.Fatal("缓存命中应成功")
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Fatalf("第二次应命中缓存，实际请求 %d 次", n)
	}

	// 失败源（500）：ok=false，且退避窗口内不重复请求
	bad, bcalls := newFakeLister(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	if _, ok := bad.dirs(t.Context(), "o", "r"); ok {
		t.Fatal("500 应判失败")
	}
	after := atomic.LoadInt32(bcalls)
	if after == 0 {
		t.Fatal("首次失败应发生过请求")
	}
	if _, ok := bad.dirs(t.Context(), "o", "r"); ok {
		t.Fatal("失败后仍应判失败")
	}
	if n := atomic.LoadInt32(bcalls); n != after {
		t.Fatalf("失败应进负缓存（不重试）: %d → %d 次请求", after, n)
	}
	if d := iconAliasBackoff(1); d != 30*time.Minute {
		t.Errorf("首次退避 = %v", d)
	}
	if d := iconAliasBackoff(9); d != iconAliasFailMax {
		t.Errorf("退避应封顶 6h，实际 %v", d)
	}
}

// H. main 不可用时回落 master（老仓库默认分支）。
func TestIconAliasListerMasterFallback(t *testing.T) {
	l, _ := newFakeLister(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "@master/flat") {
			_, _ = w.Write([]byte(flatJSON(filesKKKK987...)))
			return
		}
		http.NotFound(w, r)
	})
	e, ok := l.dirs(t.Context(), "KKKK987", "FnDepot")
	if !ok || aliasDir(e, "小松鼠") != "xiansongshu" {
		t.Fatalf("master 回落失败: ok=%v e=%+v", ok, e)
	}
}

// I. 空清单视为失败（jsDelivr 对未收录包返回 files: []）。
func TestIconAliasListerEmptyIsFailure(t *testing.T) {
	l, _ := newFakeLister(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"files":[]}`))
	})
	if _, ok := l.dirs(t.Context(), "o", "r"); ok {
		t.Fatal("空清单应判失败（降级）")
	}
}

// 测试用 Server（Cfg.Sources 供 sourceBaseURL 解析；Mirrors 供 mirrorVariants
// 取健康镜像序——生产恒非 nil，测试不注入会 nil panic）。
func newIconAliasTestServer(t *testing.T, srcs ...config.SourceRef) *Server {
	t.Helper()
	cfg := &config.Config{Sources: srcs}
	return &Server{Cfg: cfg, Src: source.NewManager(cfg), Mirrors: NewMirrorMonitor()}
}

// J. 非 GitHub 源不触发：连目录列举都不该发生。
func TestResolveIconAliasNonGitHubSkips(t *testing.T) {
	l, calls := newFakeLister(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("非 GitHub 源不应请求列举: %s", r.URL)
	})
	iconAliasTestHook = l
	t.Cleanup(func() { iconAliasTestHook = nil })
	s := newIconAliasTestServer(t, config.SourceRef{Name: "gitea", URL: "http://192.0.2.15:3033/bluemink/FnDepot"})
	if _, _, ok := s.resolveIconAlias(t.Context(), &source.App{Name: "moo", Source: "gitea"}, map[string]bool{}); ok {
		t.Fatal("非 GitHub 源不应命中")
	}
	if n := atomic.LoadInt32(calls); n != 0 {
		t.Fatalf("非 GitHub 源不应发请求，实际 %d 次", n)
	}
}

// K. 列举接口失败 → 静默降级（维持原 404 行为，不 panic、不阻塞）。
func TestResolveIconAliasDegradesOnListFailure(t *testing.T) {
	l, _ := newFakeLister(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // jsDelivr 50MB 超限实测就是 403
	})
	iconAliasTestHook = l
	t.Cleanup(func() { iconAliasTestHook = nil })
	s := newIconAliasTestServer(t, config.SourceRef{Name: "brian099-fn-fpk-packages", URL: "https://github.com/Brian099/fn_fpk_packages"})
	def := map[string]bool{}
	if _, _, ok := s.resolveIconAlias(t.Context(), &source.App{Name: "music-toolkit", Source: "brian099-fn-fpk-packages"}, def); ok {
		t.Fatal("列举失败应降级为不命中")
	}
	if len(def) != 0 {
		t.Fatalf("列举失败不得写负缓存证据: %v", def)
	}
}

// L. 多目录仓库无匹配 → 不命中（反向：不把别的应用图标配给缺失 key）。
func TestResolveIconAliasNoMatchKeeps404(t *testing.T) {
	l, _ := newFakeLister(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(flatJSON(filesMultiDir...)))
	})
	iconAliasTestHook = l
	t.Cleanup(func() { iconAliasTestHook = nil })
	s := newIconAliasTestServer(t, config.SourceRef{Name: "710850609", URL: "https://github.com/710850609/FnDepot"})
	for _, name := range []string{"music-toolkit", "Nginx", "DDNS-GO", "lobechat"} {
		if _, _, ok := s.resolveIconAlias(t.Context(), &source.App{Name: name, Source: "710850609"}, map[string]bool{}); ok {
			t.Errorf("%s 不该命中别名目录", name)
		}
	}
}

// M. 端到端（假 jsDelivr + 假 raw 主机 + netguard 豁免）：命中 → 取图 → 返回
// 真 PNG 字节；反例 = 候选全 404 时不命中，但把确定性缺失证据交给调用方
// （resolveIcon 据此写负缓存，不再每轮重赛）。
func TestResolveIconAliasEndToEnd(t *testing.T) {
	// 127.0.0.1 的 netguard 豁免由本包 TestMain 统一注册（testmain_test.go），
	// 这里绝不能 SetRebuilder(nil)——会把整包的豁免清掉、误伤后续测试。
	var hits int32
	raw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.URL.Path == "/33205q/FnDepot/main/SmartDNS/ICON.PNG" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(testPNG)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(raw.Close)
	raw404 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(raw404.Close)

	oldBase := iconAliasRawBase
	t.Cleanup(func() { iconAliasRawBase = oldBase })
	iconAliasRawBase = raw.URL

	l, _ := newFakeLister(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(flatJSON(files33205q...)))
	})
	iconAliasTestHook = l
	t.Cleanup(func() { iconAliasTestHook = nil })

	s := newIconAliasTestServer(t, config.SourceRef{Name: "33205q", URL: "https://github.com/33205q/FnDepot"})
	data, ct, ok := s.resolveIconAlias(t.Context(), &source.App{Name: "smartdns", Source: "33205q"}, map[string]bool{})
	if !ok {
		t.Fatalf("应命中 SmartDNS 图标（raw 命中 %d 次）", atomic.LoadInt32(&hits))
	}
	if len(data) < 8 || data[0] != 0x89 || data[1] != 'P' || data[2] != 'N' || data[3] != 'G' {
		t.Fatalf("返回字节非 PNG 魔数（%d 字节）", len(data))
	}
	if ct != "image/png" {
		t.Errorf("Content-Type = %q，want image/png", ct)
	}

	// 反例：raw 全 404 → 不命中 + 确定性缺失证据可回传（负缓存）
	iconAliasRawBase = raw404.URL
	def := map[string]bool{}
	if _, _, ok := s.resolveIconAlias(t.Context(), &source.App{Name: "smartdns", Source: "33205q"}, def); ok {
		t.Fatal("raw 全 404 时不应命中")
	}
	if len(def) == 0 {
		t.Fatal("确定缺失应写入 defMissPaths（供 resolveIcon 记负缓存）")
	}
}

// N. 真实网络端到端（默认跳过）：5 个真实缺口 + 1 个反例。
// 运行：MOO_ICON_ALIAS_LIVE=1 go test ./internal/api/ -run TestIconAliasLive -v
func TestIconAliasLive(t *testing.T) {
	if os.Getenv("MOO_ICON_ALIAS_LIVE") != "1" {
		t.Skip("设 MOO_ICON_ALIAS_LIVE=1 才跑真实网络校验")
	}
	cases := []struct {
		source, url, app string
		wantHit          bool
	}{
		{"33205q", "https://github.com/33205q/FnDepot", "smartdns", true},
		{"710850609", "https://github.com/710850609/FnDepot", "smartdns", true},
		{"ryxiang", "https://github.com/ryxiang/FnDepot", "smartdns", true},
		{"tzi-shue-fndepot-1", "https://github.com/tzi-shue/FnDepot-1", "smartdns", true},
		{"KKKK987", "https://github.com/KKKK987/FnDepot", "小松鼠", true},
		// 反例①：该仓库超 jsDelivr 50MB 上限（实测 403）→ 必须静默降级
		{"brian099-fn-fpk-packages", "https://github.com/Brian099/fn_fpk_packages", "music-toolkit", false},
		// 反例②：java-* 的 ICON.PNG 是上游提交的 404 占位图 → 必须被黑名单拒绝
		{"SaucdX233", "https://github.com/SaucdX233/FnDepot", "java-11-openjdk", false},
		{"xianyu110", "https://github.com/xianyu110/FnDepot", "java-8-openjdk", false},
		{"LI1223081906", "https://github.com/LI1223081906/FnDepot", "java-25-openjdk", false},
		// 反例③：仓库目录与 appname 无关（lobechat 包在仓库根）→ 不得配错图标
		{"kalisgp-cmyk", "https://github.com/kalisgp-cmyk/FnDepot", "lobechat", false},
		// 反例④：仓库列举拿不到（jsDelivr 403）→ 静默降级
		{"Hxido-RXM", "https://github.com/Hxido-RXM/FnDepot", "Nginx", false},
	}
	l := newIconAliasLister(filepath.Join(t.TempDir(), "live.json"))
	iconAliasTestHook = l
	t.Cleanup(func() { iconAliasTestHook = nil })
	var srcs []config.SourceRef
	for _, c := range cases {
		srcs = append(srcs, config.SourceRef{Name: c.source, URL: c.url})
	}
	s := newIconAliasTestServer(t, srcs...)
	for _, c := range cases {
		owner, repo, _ := iconAliasRepo(c.url)
		ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
		e, listed := l.dirs(ctx, owner, repo)
		dir := ""
		if listed {
			dir = aliasDir(e, c.app)
		}
		def := map[string]bool{}
		data, ct, hit := s.resolveIconAlias(ctx, &source.App{Name: c.app, Source: c.source}, def)
		cancel()
		t.Logf("%-28s jsdelivr=%v dir=%-12q 命中=%-5v 字节=%-7d ct=%-10s defMiss=%d",
			c.source, listed, dir, hit, len(data), ct, len(def))
		if hit != c.wantHit {
			t.Errorf("%s/%s 命中=%v，want %v", c.source, c.app, hit, c.wantHit)
		}
		if hit && (len(data) < 8 || data[0] != 0x89) {
			t.Errorf("%s 命中但非 PNG 魔数（%d 字节）", c.source, len(data))
		}
	}
}
