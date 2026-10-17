package api

// repair_326_test.go —— 0.6.326 上游容错两项的定向单测：
//
//	A) repairPngUTF8Mangle：只修「0x89 被 UTF-8 转成 U+FFFD」且 IHDR+CRC
//	   完好的 PNG，其余一律不修（保持原有拒收行为）；
//	B) readmeRepoPageCandidates：只把 GitHub「仓库首页 / tree/<br>」展开成
//	   README 文件直链；/blob/ 直链与其它站点不展开（不猜）。

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"strings"
	"testing"
)

// pngHeader 造一段结构合法的 PNG 头部（签名 + IHDR + CRC，33 字节）。
func pngHeader() []byte {
	out := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	data := make([]byte, 13)
	binary.BigEndian.PutUint32(data[0:4], 1) // width
	binary.BigEndian.PutUint32(data[4:8], 1) // height
	data[8], data[9], data[10], data[11], data[12] = 8, 2, 0, 0, 0
	var ln [4]byte
	binary.BigEndian.PutUint32(ln[:], 13)
	out = append(out, ln[:]...)
	chunk := append([]byte("IHDR"), data...)
	out = append(out, chunk...)
	var sum [4]byte
	binary.BigEndian.PutUint32(sum[:], crc32.ChecksumIEEE(chunk))
	return append(out, sum[:]...)
}

func TestRepairPngUTF8Mangle_326(t *testing.T) {
	orig := pngHeader()
	mangled := append([]byte{0xEF, 0xBF, 0xBD}, orig[1:]...)

	got := repairPngUTF8Mangle(mangled)
	if !bytes.Equal(got, orig) {
		t.Fatalf("应修复为原始 PNG 头：got=%x", got[:12])
	}

	// CRC 损坏 → 不修
	bad := append([]byte{}, mangled...)
	bad[len(bad)-1] ^= 0xFF
	if repairPngUTF8Mangle(bad) != nil {
		t.Fatal("CRC 不匹配时不得修复（避免把坏数据当图片）")
	}

	// 类型不是 IHDR → 不修
	bad2 := append([]byte{}, mangled...)
	copy(bad2[3+11:3+15], "IDAT")
	if repairPngUTF8Mangle(bad2) != nil {
		t.Fatal("IHDR 类型不符时不得修复")
	}

	// 正常 PNG（已是 0x89 开头）→ 不处理（幂等，不重复修）
	if repairPngUTF8Mangle(orig) != nil {
		t.Fatal("正常 PNG 不应被改动")
	}

	// HTML / 任意字节 → 不修
	if repairPngUTF8Mangle([]byte("<html><body>404</body></html>")) != nil {
		t.Fatal("非图片字节不得修复")
	}
}

func TestReadmeRepoPageCandidates_326(t *testing.T) {
	cands, ok := readmeRepoPageCandidates("https://github.com/cmbya/ZJJX")
	if !ok || len(cands) != 4 {
		t.Fatalf("仓库首页应展开 4 个候选（main/master × README.md/readme.md）：%v ok=%v", cands, ok)
	}
	for _, u := range cands {
		if strings.Contains(u, "/tree/") || u == "https://github.com/cmbya/ZJJX" {
			t.Fatalf("候选里不得包含页面 URL：%v", cands)
		}
		if !strings.HasPrefix(u, "https://raw.githubusercontent.com/cmbya/ZJJX/") {
			t.Fatalf("候选应为 raw 直链：%v", cands)
		}
	}

	// 指定分支：只试该分支
	cands2, ok2 := readmeRepoPageCandidates("https://github.com/cmbya/ZJJX/tree/dev")
	if !ok2 || len(cands2) != 2 {
		t.Fatalf("tree 分支应展开 2 个候选：%v", cands2)
	}
	for _, u := range cands2 {
		if !strings.Contains(u, "/dev/") {
			t.Fatalf("应使用 URL 指定分支：%v", cands2)
		}
	}

	// 非页面形态不展开（blob 直链由 normalizeReadmeURL 处理）
	for _, u := range []string{
		"https://github.com/cmbya/ZJJX/blob/main/README.md",
		"https://github.com/cmbya/ZJJX/issues",
		"https://gitlab.com/cmbya/ZJJX",
		"https://example.com/a/b",
	} {
		if _, ok := readmeRepoPageCandidates(u); ok {
			t.Fatalf("不应展开：%s", u)
		}
	}
}

// TestReadmeRelImagesWinnerBase_326 审计 MED-1 回归守卫：仓库页回落取到的
// README，相对图片必须按「实际命中的 raw README URL」绝对化；随后 serve 阶段
// 用页面 base 再改写一次不得把已绝对化的地址改坏（幂等）。
func TestReadmeRelImagesWinnerBase_326(t *testing.T) {
	body := []byte("![a](./img/a.png)\n\n[![b](../b.png)](https://example.com)\n")
	winner := "https://raw.githubusercontent.com/cmbya/ZJJX/main/README.md"
	out := rewriteRelImages(body, winner)
	if !bytes.Contains(out, []byte("https://raw.githubusercontent.com/cmbya/ZJJX/main/img/a.png")) {
		t.Fatalf("相对图片未按命中文件绝对化：%s", out)
	}
	again := rewriteRelImages(out, "https://github.com/cmbya/ZJJX")
	if !bytes.Equal(out, again) {
		t.Fatalf("二次改写不得改变已绝对化地址：\n%s\n%s", out, again)
	}
}

// TestReadmeWinnerBase_326 镜像命中的 winner 不能直接当相对图片 base：
// 必须剥回 GitHub 直链（否则 `![](./a.png)` 会变成镜像前缀+嵌套 https）。
func TestReadmeWinnerBase_326(t *testing.T) {
	raw := "https://raw.githubusercontent.com/o/r/main/README.md"
	cases := []struct{ in, want string }{
		{raw, raw},
		{"https://hub.conversun.com/" + raw, raw},
		{"https://ghfast.top/" + raw, raw},
		{"https://github.com/o/r/blob/main/README.md", raw},
		{"https://cdn.jsdelivr.net/gh/o/r@main/README.md", "https://cdn.jsdelivr.net/gh/o/r@main/README.md"},
	}
	for _, c := range cases {
		if got := readmeWinnerBase(c.in); got != c.want {
			t.Fatalf("readmeWinnerBase(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
