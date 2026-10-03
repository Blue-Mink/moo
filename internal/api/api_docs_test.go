package api

// api_docs_test.go —— 文档覆盖双向校验（2026-10-01 API 审计补）。
//
// 背景：docs/API.md 是「全量 API 文档」，但此前靠人工维护，出现过多达 8 个
// 端点漏记（7 个 /api/official/* + /api/apps/{key}/wizard），统计表也与代码
// 对不上（写 78、实际 85）。这里用测试把「代码注册的路由」与「文档列出的端点」
// 双向对齐，任何一侧漂移都会让 CI 直接报错。
//
// 约定：
//   - 路由来源 = 本包 *_test.go 之外的源码里所有 mux.HandleFunc("METHOD /path")；
//   - 文档来源 = ../../docs/API.md 里出现的 `METHOD /api/...`（忽略查询串）；
//   - 路径参数名归一化：{key}/{name}/{id}/… 一律视为同一形态，避免改参数名即报错。

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	routeRe = regexp.MustCompile(`mux\.HandleFunc\("([A-Z]+) ([^"]+)"`)
	docRe   = regexp.MustCompile("`?" + `\b(GET|POST|PUT|PATCH|DELETE)\s+(/api/[A-Za-z0-9_{}/.\-]+)`)
	paramRe = regexp.MustCompile(`\{[^}]*\}`)
)

// normalizeEndpoint 归一化：去查询串、路径参数名统一、去尾部标点。
func normalizeEndpoint(method, path string) string {
	path = strings.SplitN(path, "?", 2)[0]
	path = paramRe.ReplaceAllString(path, "{}")
	path = strings.TrimRight(path, ".,;:…。、")
	path = strings.TrimSuffix(path, "/")
	return method + " " + path
}

// registeredRoutes 扫本包非测试源码，汇总注册的路由。
func registeredRoutes(t *testing.T) map[string]string {
	t.Helper()
	found := map[string]string{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读取包目录失败: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", name, err)
		}
		for _, m := range routeRe.FindAllStringSubmatch(string(b), -1) {
			found[normalizeEndpoint(m[1], m[2])] = name
		}
	}
	if len(found) == 0 {
		t.Fatal("未从源码解析到任何路由，正则或目录假设已失效")
	}
	return found
}

// documentedEndpoints 从 docs/API.md 解析列出的端点。
func documentedEndpoints(t *testing.T) map[string]bool {
	t.Helper()
	docPath := filepath.Join("..", "..", "docs", "API.md")
	b, err := os.ReadFile(docPath)
	if err != nil {
		t.Skipf("未找到 %s（发布包可能不含文档）: %v", docPath, err)
	}
	found := map[string]bool{}
	for _, m := range docRe.FindAllStringSubmatch(string(b), -1) {
		found[normalizeEndpoint(m[1], m[2])] = true
	}
	if len(found) == 0 {
		t.Fatal("未从 docs/API.md 解析到任何端点，文档格式或正则已失效")
	}
	return found
}

// TestAPIDocCoversEveryRegisteredRoute：每个代码里的路由都必须在文档出现。
func TestAPIDocCoversEveryRegisteredRoute(t *testing.T) {
	routes := registeredRoutes(t)
	docs := documentedEndpoints(t)
	var missing []string
	for ep, file := range routes {
		if !docs[ep] {
			missing = append(missing, ep+"  ("+file+")")
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("以下 %d 个已注册路由未写进 docs/API.md：\n  %s", len(missing), strings.Join(missing, "\n  "))
	}
}

// TestDocEndpointsExistInCode：文档里每个端点都必须在代码里真实存在。
func TestDocEndpointsExistInCode(t *testing.T) {
	routes := registeredRoutes(t)
	docs := documentedEndpoints(t)
	var stale []string
	for ep := range docs {
		if _, ok := routes[ep]; !ok {
			stale = append(stale, ep)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Fatalf("docs/API.md 里以下 %d 个端点在代码中不存在（文档过期）：\n  %s", len(stale), strings.Join(stale, "\n  "))
	}
}

// TestSkillAPIMirrorMatchesRootDoc：skill 里的 API 镜像必须与 docs/API.md 逐字节一致。
//
// skills/moo/references/api-full.md 是 docs/API.md 的镜像（供 agent 技能加载）。
// 两份文档各改一处就会漂移，这里直接卡死；若发布包未携带 skills/ 则跳过。
func TestSkillAPIMirrorMatchesRootDoc(t *testing.T) {
	rootBytes, err := os.ReadFile(filepath.Join("..", "..", "docs", "API.md"))
	if err != nil {
		t.Skipf("未找到 docs/API.md: %v", err)
	}
	mirrorBytes, err := os.ReadFile(filepath.Join("..", "..", "skills", "moo", "references", "api-full.md"))
	if err != nil {
		t.Skipf("未找到 skill 镜像（发布包通常不含 skills/）: %v", err)
	}
	if string(rootBytes) != string(mirrorBytes) {
		t.Fatalf("docs/API.md 与 skills/moo/references/api-full.md 内容不一致——改一处必须同步另一处")
	}
}

// TestFeatureMapEndpointsExistInCode：skill 的 feature → API 索引里提到的端点必须都在代码里存在。
//
// 该索引是 agent 按功能找端点的入口（`GET/POST /api/...` 合并写法也允许），
// 端点被改名/删除后索引若不跟着改，agent 会照着调不存在的路径。
func TestFeatureMapEndpointsExistInCode(t *testing.T) {
	path := filepath.Join("..", "..", "skills", "moo", "references", "feature-map.md")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("未找到 feature-map.md（发布包通常不含 skills/）: %v", err)
	}
	combo := regexp.MustCompile(`\b((?:GET|POST|PUT|PATCH|DELETE)(?:/(?:GET|POST|PUT|PATCH|DELETE))*)\s+(/api/[A-Za-z0-9_{}/.\-]+)`)
	routes := registeredRoutes(t)
	var stale []string
	for _, m := range combo.FindAllStringSubmatch(string(b), -1) {
		for _, method := range strings.Split(m[1], "/") {
			ep := normalizeEndpoint(method, m[2])
			if _, ok := routes[ep]; !ok {
				stale = append(stale, ep)
			}
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Fatalf("feature-map.md 里以下 %d 个端点在代码中不存在：\n  %s", len(stale), strings.Join(stale, "\n  "))
	}
}
