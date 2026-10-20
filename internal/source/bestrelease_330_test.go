package source

import "testing"

// 0.6.330：bestRelease 必须确定性 —— map 迭代顺序随机，判等/不可比时也必须稳定选同一个包。
func TestBestReleaseDeterministic(t *testing.T) {
	rel := map[string]any{
		"1.0":               map[string]any{"packages": map[string]any{"x86": map[string]any{"download_url": "u-1.0"}}},
		"1.0.0":             map[string]any{"packages": map[string]any{"x86": map[string]any{"download_url": "u-1.0.0"}}},
		"1.0.0-1":           map[string]any{"packages": map[string]any{"x86": map[string]any{"download_url": "u-1.0.0-1"}}},
		"latest-2026.10.17": map[string]any{"packages": map[string]any{"x86": map[string]any{"download_url": "u-latest"}}},
	}
	firstVer, firstArch, firstPkg := bestRelease(rel, "x86")
	if firstVer != "1.0.0-1" {
		t.Fatalf("应选数值最高的 1.0.0-1，实际 %q", firstVer)
	}
	if firstArch != "x86" || firstPkg == nil {
		t.Fatalf("架构/包解析异常: arch=%q pkg=%v", firstArch, firstPkg)
	}
	for i := 0; i < 200; i++ {
		if v, a, _ := bestRelease(rel, "x86"); v != firstVer || a != firstArch {
			t.Fatalf("第 %d 次结果抖动: %q/%q vs %q/%q", i, v, a, firstVer, firstArch)
		}
	}

	// 全是判等/不可比键时，也必须稳定（字面序兜底）
	rel2 := map[string]any{
		"1.0":               map[string]any{"packages": map[string]any{"x86": map[string]any{"download_url": "u-a"}}},
		"1.0.0":             map[string]any{"packages": map[string]any{"x86": map[string]any{"download_url": "u-b"}}},
		"latest-2026.10.17": map[string]any{"packages": map[string]any{"x86": map[string]any{"download_url": "u-c"}}},
	}
	v0, _, _ := bestRelease(rel2, "x86")
	for i := 0; i < 200; i++ {
		if v, _, _ := bestRelease(rel2, "x86"); v != v0 {
			t.Fatalf("判等/不可比场景抖动: %q vs %q", v, v0)
		}
	}
}
