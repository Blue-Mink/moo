package api

// docs_version_guard_test.go —— 版本类文档的自动守护（2026-11-28 补）。
//
// 背景：README 徽章 / 安装行的版本与 sha、应用介绍里的「当前版本」、
// docs/API.md 的端点总数，长期只靠发版清单人工记；0.6.579 又漏了一次
// （README 停在 0.6.566 的徽章与旧 sha）。这里把文档版本与实际索引/路由绑定：
// 改了版本号却没改文档，CI 直接红。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func repoFile(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(append([]string{"..", ".."}, parts...)...)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", p, err)
	}
	return string(b)
}

func manifestVersion(t *testing.T) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^version\s*=\s*(\S+)\s*$`).FindStringSubmatch(repoFile(t, "fnos", "manifest"))
	if m == nil {
		t.Fatal("fnos/manifest 里没解析到 version")
	}
	return m[1]
}

func TestReadmeVersionMatchesManifest(t *testing.T) {
	ver := manifestVersion(t)
	readme := repoFile(t, "README.md")
	if !strings.Contains(readme, "badge/FPK-"+ver+"-") {
		t.Fatalf("README 徽章未跟版：期望 badge/FPK-%s-", ver)
	}
	if !strings.Contains(readme, "当前 `"+ver+"`") {
		t.Fatalf("README 安装行未跟版：期望 当前 `%s`", ver)
	}
	if !strings.Contains(repoFile(t, "应用介绍.md"), "当前版本 v"+ver) {
		t.Fatalf("应用介绍.md 未跟版：期望 当前版本 v%s", ver)
	}
}

func TestReadmeShaMatchesIndex(t *testing.T) {
	var idx struct {
		Apps map[string]struct {
			Sha256 string `json:"sha256"`
		} `json:"apps"`
	}
	if err := json.Unmarshal([]byte(repoFile(t, "moo.json")), &idx); err != nil {
		t.Fatalf("解析 moo.json 失败: %v", err)
	}
	sha := idx.Apps["moo"].Sha256
	if sha == "" {
		t.Fatal("moo.json apps.moo.sha256 为空")
	}
	if !strings.Contains(repoFile(t, "README.md"), sha) {
		t.Fatalf("README 安装行的 x86 sha 未跟版：期望包含 %s", sha)
	}
}

func TestAPIDocsEndpointCountMatchesRoutes(t *testing.T) {
	routes := registeredRoutes(t)
	doc := repoFile(t, "docs", "API.md")
	m := regexp.MustCompile(`按代码重算，共 (\d+) 条`).FindStringSubmatch(doc)
	if m == nil {
		t.Fatal("docs/API.md §1.3 未找到「按代码重算，共 N 条」")
	}
	declared, _ := strconv.Atoi(m[1])
	if declared != len(routes) {
		t.Fatalf("docs/API.md 端点总数 %d ≠ 代码注册 %d 条（增删路由要同步统计表与逐域计数）", declared, len(routes))
	}
}
