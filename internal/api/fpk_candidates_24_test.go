package api

import (
	"testing"

	"moo/internal/config"
	"moo/internal/source"
)

// 2026-11-21 FnDepot#24（B-DL-MIRROR-SINGLE-CANDIDATE）：
// 下载地址若已被镜像前缀固化（cdn.gh-proxy.org 等），旧实现只返回一条候选，
// 镜像一挂就 100% 失败。下列断言在修复前必须 FAIL（反向验证）。

func hasCand(list []string, want string) bool {
	for _, u := range list {
		if u == want {
			return true
		}
	}
	return false
}

func TestFpkCandidates24_MirrorWrappedRaw(t *testing.T) {
	inner := "https://raw.githubusercontent.com/Wyf841015/FnDepot/master/fnclearup/fnclearup.fpk"
	base := "https://cdn.gh-proxy.org/" + inner
	got := buildFPKCandidates(base, []string{"https://gh-proxy.com/", "https://gh-proxy.org/"})
	if len(got) < 2 {
		t.Fatalf("镜像前缀地址至少应展开 2 条候选（含脱壳直连），got %d: %v", len(got), got)
	}
	if !hasCand(got, inner) {
		t.Errorf("缺少脱壳后的直连候选 %s，got %v", inner, got)
	}
	if !hasCand(got, "https://gh-proxy.com/"+inner) {
		t.Errorf("缺少可用镜像候选，got %v", got)
	}
}

func TestFpkCandidates24_RawDirect(t *testing.T) {
	base := "https://raw.githubusercontent.com/yujian0815/fnmedia-ext/main/x.fpk"
	got := buildFPKCandidates(base, []string{"https://gh-proxy.com/"})
	if len(got) < 2 {
		t.Fatalf("raw 直链也应展开镜像候选，got %d: %v", len(got), got)
	}
	if got[len(got)-1] != base {
		t.Errorf("直连应排在最后，got %v", got)
	}
}

func TestFpkCandidates24_ReleasesDirect(t *testing.T) {
	base := "https://github.com/Blue-Mink/moo/releases/download/v0.6.552/moo_0.6.552_x86.fpk"
	got := buildFPKCandidates(base, []string{"https://gh-proxy.com/"})
	if len(got) != 2 || got[1] != base {
		t.Fatalf("releases 直链期望 [镜像, 直连]，got %v", got)
	}
}

func TestFpkCandidates24_NonGitHubPassthrough(t *testing.T) {
	base := "https://example.com/app/x.fpk"
	got := buildFPKCandidates(base, []string{"https://gh-proxy.com/"})
	if len(got) != 1 || got[0] != base {
		t.Fatalf("非 GitHub 主机应原样返回单条，got %v", got)
	}
}

func TestFpkCandidates24_Empty(t *testing.T) {
	if got := buildFPKCandidates("", []string{"https://gh-proxy.com/"}); got != nil {
		t.Fatalf("空地址应返回 nil，got %v", got)
	}
}

func TestFpkCandidates24_NoDuplicate(t *testing.T) {
	inner := "https://raw.githubusercontent.com/o/r/main/x.fpk"
	base := "https://gh-proxy.com/" + inner
	got := buildFPKCandidates(base, []string{"https://gh-proxy.com/"})
	seen := map[string]bool{}
	for _, u := range got {
		if seen[u] {
			t.Errorf("候选重复：%s（全量 %v）", u, got)
		}
		seen[u] = true
	}
	if len(got) < 2 {
		t.Errorf("去重不应把脱壳直连吃掉，got %v", got)
	}
}

// P0-1：候选覆盖告警的判定边界（纯函数）。
func TestMirrorCoverageGap24(t *testing.T) {
	cases := []struct {
		name                     string
		prefixes, out            int
		accelerable, forceDirect bool
		want                     bool
	}{
		{"可加速 + 无前缀", 0, 1, true, false, true},
		{"可加速 + 有前缀但只剩一条", 3, 1, true, false, true},
		{"可加速 + 正常 2 条", 1, 2, true, false, false},
		{"可加速 + 强制直连", 0, 1, true, true, false},
		{"不可加速", 0, 1, false, false, false},
	}
	for _, c := range cases {
		if got := mirrorCoverageGap(c.prefixes, c.out, c.accelerable, c.forceDirect); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

// Server 级（实例路径）：镜像壳地址经真实 Server 后必须得到 ≥2 条候选。
func TestFpkCandidates24_ServerLevel(t *testing.T) {
	// 用 direct 模式避免测试里触发远端清单拉取（auto 会等网络超时，实测 35s）；
	// 镜像前缀展开路径已由上面的纯函数用例覆盖，这里只验证 Server 路径的脱壳与兜底。
	s := &Server{Cfg: &config.Config{Mirror: "direct"}, Mirrors: NewMirrorMonitor()}
	base := "https://cdn.gh-proxy.org/https://raw.githubusercontent.com/o/r/main/x.fpk"
	got := s.FpkCandidates(&source.App{Name: "x", DownloadURL: base})
	if len(got) < 2 {
		t.Fatalf("Server 级候选不足（%d）：%v", len(got), got)
	}
	inner := "https://raw.githubusercontent.com/o/r/main/x.fpk"
	if !hasCand(got, inner) {
		t.Errorf("缺少脱壳直连：%v", got)
	}
	if got[len(got)-1] != base {
		t.Errorf("原地址应排最后兜底，got %v", got)
	}
}

// 盲点（实例测试暴露）：镜像壳内层不是 GitHub 主机时，也必须脱壳。
func TestFpkCandidates24_MirrorWrappedGenericInner(t *testing.T) {
	inner := "http://127.0.0.1:38302/fnclearup.fpk"
	base := "https://cdn.gh-proxy.org/" + inner
	got := buildFPKCandidates(base, []string{"https://gh-proxy.com/"})
	if len(got) != 2 {
		t.Fatalf("非 GitHub 内层应得到 [内层, 原壳] 两条，got %v", got)
	}
	if got[0] != inner || got[1] != base {
		t.Fatalf("顺序应为 [内层, 原壳]，got %v", got)
	}
}

// 自定义反向代理可能带路径前缀（https://proxy.example.com/gh/<https://github.com/…>），
// 那是用户唯一可达的通道，不能被脱壳逻辑丢掉：原地址必须保留为最后兜底。
func TestFpkCandidates24_PathPrefixedProxyKept(t *testing.T) {
	base := "https://proxy.example.com/gh/https://github.com/o/r/releases/download/v1/x.fpk"
	got := buildFPKCandidates(base, []string{"https://gh-proxy.com/"})
	if len(got) == 0 || got[len(got)-1] != base {
		t.Fatalf("带路径前缀的自定义代理被丢弃：%v", got)
	}
}

// 2026-11-22 清理死源后的回归加固：老配置里选中的 key 被下架、冷启动又无测速数据时，
// 前缀补位必须保证仍有多个镜像候选，而不是退化成单条直连。
func TestFillMirrorPrefixes24_RemovedKeyColdStart(t *testing.T) {
	got := fillMirrorPrefixes(nil, 3)
	if len(got) != 3 {
		t.Fatalf("应补足 3 个前缀，got %v", got)
	}
	if got[0] != "https://gh-proxy.com/" {
		t.Errorf("应按清单顺序补位，got %v", got)
	}
	again := fillMirrorPrefixes([]string{"https://gh-proxy.com/"}, 3)
	if len(again) != 3 || again[0] != "https://gh-proxy.com/" {
		t.Fatalf("已有前缀不应重复且需补足，got %v", again)
	}
	seen := map[string]bool{}
	for _, p := range again {
		if seen[p] {
			t.Errorf("补位出现重复：%v", again)
		}
		seen[p] = true
	}
}
