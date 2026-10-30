package api

import (
	"bytes"
	"testing"
)

// 0.6.377 批 2：图片完整性加固（全量测试 F5b 盲点）。
// ① 去掉「<1024 B 一律放行」：900 B 截断 JPEG（无 Content-Length 时）
//
//	正是从这条豁免漏进缓存的（缓存 7 天 → 图永久加载不出来）。
//
// ② 合法极小图必须仍然通过，不能为了严格而误杀。
func TestImageCompleteRemovesTinyExemption_377(t *testing.T) {
	// 900 B 截断 JPEG：魔数对、无 EOI 尾巴（F5b 原样复现）
	jpg900 := append([]byte{0xFF, 0xD8}, bytes.Repeat([]byte{0x11}, 896)...)
	if imageComplete(jpg900) {
		t.Fatal("900 B 截断 JPEG 必须判截断（旧 <1024 豁免会把 900 B 一律放行）")
	}
	// 极小但合法：1×1 PNG 68 B（带 IEND 尾块）
	tinyPNG := append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A},
		bytes.Repeat([]byte{0x00}, 48)...)
	tinyPNG = append(tinyPNG, 0x00, 0x00, 0x00, 0x00, 'I', 'E', 'N', 'D', 0xAE, 'B', '`', 0x82)
	if !imageComplete(tinyPNG) {
		t.Fatalf("合法极小 PNG（%d B，带 IEND）应判定完整", len(tinyPNG))
	}
	// 极小但合法：43 B GIF89a（以 0x3B 收尾）
	tinyGIF := append([]byte("GIF89a"), bytes.Repeat([]byte{0x00}, 36)...)
	tinyGIF = append(tinyGIF, 0x3B)
	if len(tinyGIF) != 43 {
		t.Fatalf("夹具长度应为 43，实际 %d", len(tinyGIF))
	}
	if !imageComplete(tinyGIF) {
		t.Fatal("合法 43 B GIF 应判定完整")
	}
	// 空 body / 短于 16 B：一律残缺
	if imageComplete(nil) {
		t.Fatal("空 body 不应判定完整")
	}
	if imageComplete([]byte{0xFF, 0xD8, 0xFF, 0xD9}) {
		t.Fatal("<16 B 的畸形 body 不应判定完整")
	}
}

// respLenOK：无长度头 / 超 4MB 限读区间时不比对，其余必须逐字节相符。
func TestRespLenOK_377(t *testing.T) {
	cases := []struct {
		name     string
		got      int64
		declared int64
		want     bool
	}{
		{"无Content-Length不比对", 900, -1, true},
		{"Content-Length=0不比对", 0, 0, true},
		{"超4MB限读区间不比对", 4 << 20, 9 << 20, true},
		{"长度相符", 4096, 4096, true},
		{"短于声明", 900, 4096, false},
		{"长于声明", 4097, 4096, false},
	}
	for _, c := range cases {
		if got := respLenOK(c.got, c.declared); got != c.want {
			t.Fatalf("%s: respLenOK(%d,%d)=%v，期望 %v", c.name, c.got, c.declared, got, c.want)
		}
	}
}
