package api

// mirror_fingerprint_351_test.go —— 0.6.351（方案 A）「脚本同步（非 fork）
// 搬运」内容指纹溯源兜底的单测。
//
// 复用 lkServer / mkURL / srcCache（lineage_326k_test.go）。指纹只依赖
// (appname 归一, sha256) + 源 owner，不碰 fork 缓存，故 forks=nil。

import (
	"testing"

	"moo/internal/config"
	"moo/internal/source"
)

// mkFp 造带 sha256 的卡（复用 mkURL，再补 Sha256/Distributor）。
func mkFp(key, appname, src, ver, url, sha, dist string) AppInfo {
	a := mkURL(key, appname, src, ver, url)
	a.Sha256 = sha
	a.Distributor = dist
	return a
}

func TestMirrorFingerprint_Basic(t *testing.T) {
	s := lkServer(t,
		[]config.SourceRef{{Name: "真源", URL: "https://github.com/alice/awesome-apps"}, {Name: "镜像源", URL: "https://github.com/bob/mirror"}},
		map[string]map[string]*source.App{"真源": srcCache(map[string]string{"app-a": "2.0.0"}), "镜像源": srcCache(map[string]string{"app-a": "2.0.0"})},
		nil)
	out := []AppInfo{
		mkFp("app-a@真源", "app-a", "真源", "2.0.0", "", "cafebabe", "alice"),
		mkFp("app-a@镜像源", "app-a", "镜像源", "2.0.0", "", "cafebabe", "alice"), // dist=真源 owner
	}
	s.applyMirrorFingerprint(out)
	if out[0].UpstreamRepo != "" {
		t.Fatalf("真源不应被溯源：%+v", out[0].UpstreamRepo)
	}
	if out[1].UpstreamRepo != "alice/awesome-apps" || out[1].UpstreamSource != "真源" || out[1].UpstreamVersion != "2.0.0" {
		t.Fatalf("镜像源应溯源到真源：%+v", out[1])
	}
}

func TestMirrorFingerprint_SameOwnerSkips(t *testing.T) {
	// 同 owner 两仓（brian099/fndepot 与 brian099/fn_fpk_packages）→ 分仓，不判搬运
	s := lkServer(t,
		[]config.SourceRef{{Name: "A", URL: "https://github.com/brian099/fndepot"}, {Name: "B", URL: "https://github.com/brian099/fn_fpk_packages"}},
		map[string]map[string]*source.App{"A": srcCache(map[string]string{"x": "1.0.0"}), "B": srcCache(map[string]string{"x": "1.0.0"})},
		nil)
	out := []AppInfo{
		mkFp("x@A", "x", "A", "1.0.0", "", "beef", ""),
		mkFp("x@B", "x", "B", "1.0.0", "", "beef", ""),
	}
	s.applyMirrorFingerprint(out)
	if out[0].UpstreamRepo != "" || out[1].UpstreamRepo != "" {
		t.Fatalf("同 owner 不应判搬运：%+v / %+v", out[0].UpstreamRepo, out[1].UpstreamRepo)
	}
}

func TestMirrorFingerprint_EmptyShaSkips(t *testing.T) {
	// flat fnpack 老格式 sha256 为空 → 不可比，跳过（4 组双胞胎场景）
	s := lkServer(t,
		[]config.SourceRef{{Name: "真源", URL: "https://github.com/alice/awesome-apps"}, {Name: "镜像源", URL: "https://github.com/bob/mirror"}},
		map[string]map[string]*source.App{"真源": srcCache(map[string]string{"app-a": "1.0.0"}), "镜像源": srcCache(map[string]string{"app-a": "1.0.0"})},
		nil)
	out := []AppInfo{
		mkFp("app-a@真源", "app-a", "真源", "1.0.0", "", "", "alice"),
		mkFp("app-a@镜像源", "app-a", "镜像源", "1.0.0", "", "", "alice"),
	}
	s.applyMirrorFingerprint(out)
	if out[0].UpstreamRepo != "" || out[1].UpstreamRepo != "" {
		t.Fatalf("sha256 为空应跳过：%+v / %+v", out[0].UpstreamRepo, out[1].UpstreamRepo)
	}
}

func TestMirrorFingerprint_DoesNotOverwrite(t *testing.T) {
	// 已有 UpstreamRepo（fork 路径已溯源）→ 指纹不覆盖
	s := lkServer(t,
		[]config.SourceRef{{Name: "真源", URL: "https://github.com/alice/awesome-apps"}, {Name: "镜像源", URL: "https://github.com/bob/mirror"}},
		map[string]map[string]*source.App{"真源": srcCache(map[string]string{"app-a": "2.0.0"}), "镜像源": srcCache(map[string]string{"app-a": "2.0.0"})},
		nil)
	card := mkFp("app-a@镜像源", "app-a", "镜像源", "2.0.0", "", "cafebabe", "alice")
	card.UpstreamRepo = "alice/awesome-apps"
	card.UpstreamVersion = "9.9.9"
	out := []AppInfo{card, mkFp("app-a@真源", "app-a", "真源", "2.0.0", "", "cafebabe", "alice")}
	s.applyMirrorFingerprint(out)
	if out[0].UpstreamVersion != "9.9.9" {
		t.Fatalf("已有溯源不应被覆盖：%+v", out[0].UpstreamVersion)
	}
}

func TestMirrorFingerprint_TwinDeterministic(t *testing.T) {
	// 双胞胎镜像（无打包者指向）→ 确定性选根，根不指向他人，无环
	s := lkServer(t,
		[]config.SourceRef{{Name: "T1", URL: "https://github.com/whoisaisiohw/fndepot"}, {Name: "T2", URL: "https://github.com/boater-man/fndepot"}},
		map[string]map[string]*source.App{"T1": srcCache(map[string]string{"app-a": "1.0.0"}), "T2": srcCache(map[string]string{"app-a": "1.0.0"})},
		nil)
	out := []AppInfo{
		mkFp("app-a@T1", "app-a", "T1", "1.0.0", "", "cafebabe", ""),
		mkFp("app-a@T2", "app-a", "T2", "1.0.0", "", "cafebabe", ""),
	}
	s.applyMirrorFingerprint(out)
	// 确定性：repo key 字典序 "boater-man/fndepot" < "whoisaisiohw/fndepot" → T2 为根
	if out[1].UpstreamRepo != "" {
		t.Fatalf("根不应指向他人：%+v", out[1].UpstreamRepo)
	}
	if out[0].UpstreamRepo != "boater-man/fndepot" || out[0].UpstreamSource != "T2" {
		t.Fatalf("镜像侧应溯源到根：%+v", out[0])
	}
}

func TestMirrorFingerprint_DiffShaNotMirror(t *testing.T) {
	// Hxido-RXM↔shuangji66 负例：同名但 sha 不同 → 不是镜像，不判搬运
	s := lkServer(t,
		[]config.SourceRef{{Name: "H", URL: "https://github.com/Hxido-RXM/fndepot"}, {Name: "S", URL: "https://github.com/shuangji66/fndepot"}},
		map[string]map[string]*source.App{"H": srcCache(map[string]string{"app-a": "1.0.0"}), "S": srcCache(map[string]string{"app-a": "1.0.0"})},
		nil)
	out := []AppInfo{
		mkFp("app-a@H", "app-a", "H", "1.0.0", "", "sha111", ""),
		mkFp("app-a@S", "app-a", "S", "1.0.0", "", "sha222", ""),
	}
	s.applyMirrorFingerprint(out)
	if out[0].UpstreamRepo != "" || out[1].UpstreamRepo != "" {
		t.Fatalf("同名不同 sha 不应判搬运：%+v / %+v", out[0].UpstreamRepo, out[1].UpstreamRepo)
	}
}
