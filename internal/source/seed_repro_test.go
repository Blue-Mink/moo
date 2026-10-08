package source

import (
	"testing"

	"moo/internal/config"
)

// TestSeedPipelineUnique 0.6.248 回归：首装种子管道（main.go 同款逻辑）
// 对内置源（0.6.314 起 157）必须产出 157 个唯一源名——基准集含 7 组
// 同 owner 双仓库（命名规则取 owner 会重名），旧逻辑重名折叠丢 7 条（149）。
func TestSeedPipelineUnique(t *testing.T) {
	urls := BundledDefaultSources()
	if len(urls) != 157 {
		t.Fatalf("内置基准集应为 157 源，实际 %d", len(urls))
	}
	var seeded []config.SourceRef
	taken := make(map[string]bool, len(urls))
	for _, u := range urls {
		name := UniqueSourceName(u, taken)
		if name == "" {
			t.Fatalf("源 %q 推导出空名", u)
		}
		if taken[name] {
			t.Fatalf("种子重名: %q（url=%s）", name, u)
		}
		taken[name] = true
		seeded = append(seeded, config.SourceRef{Name: name, URL: u})
	}
	if len(seeded) != 157 {
		t.Fatalf("种子应 157 条，实际 %d", len(seeded))
	}
}

// TestUniqueSourceNameOwnerPairs 0.6.248：基准集 7 组同 owner 双仓库的
// 具体命名——第二仓库必须落到 owner-repo 归一名，且互不冲突。
func TestUniqueSourceNameOwnerPairs(t *testing.T) {
	cases := []struct {
		first, second string
		wantSecond    string
	}{
		{"https://github.com/tzi-shue/FnDepot-1", "https://github.com/tzi-shue/FnDepot", "tzi-shue-fndepot"},
		{"https://github.com/33205q/FnDepot", "https://github.com/33205q/FnDepot-", "33205q-fndepot"},
		{"https://github.com/roc0838/FnDepot", "https://github.com/roc0838/FnDepot1", "roc0838-fndepot1"},
		{"https://github.com/Brian099/FnDepot", "https://github.com/Brian099/fn_fpk_packages", "brian099-fn-fpk-packages"},
		{"https://github.com/qilin-zhu/FnDepot", "https://github.com/qilin-zhu/LitePan-fpk", "qilin-zhu-litepan-fpk"},
		{"https://github.com/Soley911/FnDepot", "https://github.com/Soley911/fn", "soley911-fn"},
		{"https://github.com/seesky100825/FnDepot", "https://github.com/seesky100825/FnDepot1", "seesky100825-fndepot1"},
	}
	for _, c := range cases {
		taken := map[string]bool{}
		n1 := UniqueSourceName(c.first, taken)
		taken[n1] = true
		n2 := UniqueSourceName(c.second, taken)
		if n2 != c.wantSecond {
			t.Errorf("%s → %q，期望 %q", c.second, n2, c.wantSecond)
		}
		if n1 == n2 {
			t.Errorf("重名: %q == %q", n1, n2)
		}
	}
}

// TestUniqueSourceNameFallbacks 非 GitHub 地址与极端冲突（仓库名归一后
// 仍撞名）的退路：-2/-3 递增后缀。
func TestUniqueSourceNameFallbacks(t *testing.T) {
	taken := map[string]bool{"mylist": true, "mylist-2": true}
	if got := UniqueSourceName("https://example.com/feeds/mylist", taken); got != "feeds-mylist" {
		t.Errorf("非 GitHub 冲突 → %q，期望 feeds-mylist", got)
	}
	// 极端：父+末段归一名也被占 → -2 递增
	taken2 := map[string]bool{"mylist": true, "feeds-mylist": true, "feeds-mylist-2": true}
	if got := UniqueSourceName("https://example.com/feeds/mylist", taken2); got != "feeds-mylist-3" {
		t.Errorf("极端冲突 → %q，期望 feeds-mylist-3", got)
	}
	// github 仓库名归一：下划线/大小写/尾 -
	if got := UniqueSourceName("https://github.com/a/B_C", map[string]bool{"a": true}); got != "a-b-c" {
		t.Errorf("归一名 → %q，期望 a-b-c", got)
	}
}
