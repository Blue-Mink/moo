package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"moo/internal/config"
)

// 批次⑤（P2-2）：加速源清单单一真源守卫。
// lists/github_mirrors.txt 是唯一真源；mate 的静态兜底表 githubMirrorPrefixes 与
// Moo 的 config.GitHubMirrorOptions() 都必须与它一致（排除 conversun：
// 仅代理 conversun 仓库，与 auto/custom/direct 一样不是通用下载前缀）。
// 反向验证：从 githubMirrorPrefixes 删掉 edgeone 一条 → 本文件必 FAIL。

// readMirrorList24 解析清单：跳过注释/空行、URL 为空（auto/custom/direct）与 conversun。
func readMirrorList24(t *testing.T) (keys, urls []string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "lists", "github_mirrors.txt"))
	if err != nil {
		t.Fatalf("读取清单失败：%v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 3 {
			t.Fatalf("清单行格式错误（应为 key|label|url|desc）：%q", line)
		}
		key := strings.TrimSpace(parts[0])
		u := strings.TrimSpace(parts[2])
		if u == "" || key == "conversun" {
			continue
		}
		keys = append(keys, key)
		urls = append(urls, u)
	}
	if len(urls) == 0 {
		t.Fatalf("lists/github_mirrors.txt 未解析出任何镜像")
	}
	return keys, urls
}

// TestGitHubMirrorPrefixesMatchList24 静态兜底表 == 清单（顺序 + 内容）。
func TestGitHubMirrorPrefixesMatchList24(t *testing.T) {
	_, urls := readMirrorList24(t)
	if len(githubMirrorPrefixes) != len(urls) {
		t.Fatalf("条数不一致：静态表 %d 条，清单 %d 条\n静态表=%v\n清单=%v",
			len(githubMirrorPrefixes), len(urls), githubMirrorPrefixes, urls)
	}
	for i := range urls {
		if githubMirrorPrefixes[i] != urls[i] {
			t.Fatalf("第 %d 条不一致：静态表 %s，清单 %s\n静态表=%v\n清单=%v",
				i, githubMirrorPrefixes[i], urls[i], githubMirrorPrefixes, urls)
		}
	}
}

// TestConfigMirrorOptionsMatchList24 Moo 内置清单 == 同一份清单（集合必须相等；
// 顺序一致时顺带断言顺序，顺序不同只记录不失败——服务端允许按健康度重排）。
func TestConfigMirrorOptionsMatchList24(t *testing.T) {
	keys, urls := readMirrorList24(t)
	var gotKeys, gotURLs []string
	for _, o := range config.GitHubMirrorOptions() {
		if o.URL == "" || o.Key == "conversun" {
			continue
		}
		gotKeys = append(gotKeys, o.Key)
		gotURLs = append(gotURLs, o.URL)
	}
	if len(gotURLs) != len(urls) {
		t.Fatalf("条数不一致：config %d 条，清单 %d 条\nconfig=%v\n清单=%v",
			len(gotURLs), len(urls), gotURLs, urls)
	}
	if !sameSet24(gotURLs, urls) {
		t.Fatalf("URL 集合不一致：\nconfig=%v\n清单=%v", sorted24(gotURLs), sorted24(urls))
	}
	if !sameSet24(gotKeys, keys) {
		t.Fatalf("key 集合不一致：\nconfig=%v\n清单=%v", sorted24(gotKeys), sorted24(keys))
	}
	for i := range urls {
		if gotURLs[i] != urls[i] {
			t.Logf("顺序不同（允许）：位置 %d config=%s 清单=%s", i, gotURLs[i], urls[i])
			return
		}
	}
}

func sorted24(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func sameSet24(a, b []string) bool {
	sa, sb := sorted24(a), sorted24(b)
	if len(sa) != len(sb) {
		return false
	}
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}

// TestMirrorCandidatesWith24 健康表前缀展开：顺序、去重、直连最后、非 GitHub 原样。
func TestMirrorCandidatesWith24(t *testing.T) {
	const raw = "https://github.com/Blue-Mink/moo/releases/download/v1/x.fpk"
	got := mirrorCandidatesWith([]string{"https://a.example/", "https://a.example", " ", "https://b.example/"}, raw)
	want := []string{
		"https://a.example/" + raw,
		"https://b.example/" + raw,
		raw,
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("顺序/去重不符，want %v got %v", want, got)
	}
	if !hasMirrorPrefix(got, raw) {
		t.Fatalf("应含镜像前缀：%v", got)
	}

	// provider 为空 → 只有直连，调用方据此回落静态表。
	if got := mirrorCandidatesWith(nil, raw); len(got) != 1 || got[0] != raw {
		t.Fatalf("空前缀应只有直连，got %v", got)
	}
	if hasMirrorPrefix(mirrorCandidatesWith(nil, raw), raw) {
		t.Fatalf("空前缀不应判为有镜像")
	}

	// 非 GitHub 地址不加前缀。
	if got := mirrorCandidatesWith([]string{"https://a.example/"}, "https://example.com/a.fpk"); len(got) != 1 {
		t.Fatalf("非 GitHub 地址应原样，got %v", got)
	}

	// 静态表语义不变：首个候选 = 清单第一条前缀 + raw，直连最后。
	static := mirrorCandidates(raw)
	if !strings.HasPrefix(static[0], githubMirrorPrefixes[0]) || static[len(static)-1] != raw {
		t.Fatalf("静态候选顺序异常：%v", static)
	}
}
