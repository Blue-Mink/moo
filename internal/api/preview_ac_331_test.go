package api

import "testing"

// 0.6.331 A：jsDelivr → raw 反推。
func TestJsdelivrToRaw(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			"https://cdn.jsdelivr.net/gh/Hxido-RXM/FnDepot@main/1Panel/Preview/1.png",
			"https://raw.githubusercontent.com/Hxido-RXM/FnDepot/main/1Panel/Preview/1.png",
		},
		{
			"https://cdn.jsdelivr.net/gh/o/r@v1.2.3/a/b.png",
			"https://raw.githubusercontent.com/o/r/v1.2.3/a/b.png",
		},
		{
			"https://cdn.jsdelivr.net/gh/o/r@abc1234/x.png?foo=1",
			"https://raw.githubusercontent.com/o/r/abc1234/x.png",
		},
		{"https://cdn.jsdelivr.net/gh/o/r/x.png", ""}, // 无 @ref，无法推定分支
		{"https://cdn.jsdelivr.net/gh/o/r@main", ""},  // 无文件路径
		{"https://raw.githubusercontent.com/o/r/main/x.png", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := jsdelivrToRaw(c.in); got != c.want {
			t.Fatalf("jsdelivrToRaw(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

// 0.6.331 A：候选生成必须把 jsDelivr 形式补出 raw 直链（此前只有 1 个候选）。
func TestReadmeCandidatesIncludeRawForJsdelivr(t *testing.T) {
	s := &Server{}
	got := s.readmeCandidateURLs("https://cdn.jsdelivr.net/gh/o/r@main/README.md", false)
	want := "https://raw.githubusercontent.com/o/r/main/README.md"
	for _, u := range got {
		if u == want {
			return
		}
	}
	t.Fatalf("候选缺 raw 直链 %q，实际=%v", want, got)
}

// 反例：raw 输入不因反推逻辑被改写（原有正向展开保持）。
func TestReadmeCandidatesRawUnchanged(t *testing.T) {
	s := &Server{}
	got := s.readmeCandidateURLs("https://raw.githubusercontent.com/o/r/main/README.md", false)
	if len(got) == 0 || got[0] != "https://raw.githubusercontent.com/o/r/main/README.md" {
		t.Fatalf("首候选应保持原 raw 地址，实际=%v", got)
	}
	for _, u := range got {
		if u == "" {
			t.Fatalf("候选含空串: %v", got)
		}
	}
}
