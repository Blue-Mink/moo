package source

import "testing"

const testRoot = "github.com/ctllo-bit/fndepot"

// TestSourceURLKey（0.6.198）：与前端 sourceKey.ts 同组用例，钉死双端一致。
func TestSourceURLKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// 仓库根
		{"https://github.com/ctllo-bit/FnDepot", testRoot},
		{"https://github.com/ctllo-bit/FnDepot/", testRoot},
		{"https://github.com/ctllo-bit/FnDepot.git", testRoot},
		{"github.com/ctllo-bit/FnDepot", testRoot},
		{"HTTPS://GITHUB.COM/ctllo-bit/FnDepot", testRoot},
		{"https://github.com/ctllo-bit/FnDepot/tree/main", testRoot},
		{"https://github.com/ctllo-bit/FnDepot?tab=code", testRoot},
		// raw 前缀（任意分支/文件）
		{"https://raw.githubusercontent.com/ctllo-bit/FnDepot/main/fnpack.json", testRoot},
		{"https://raw.githubusercontent.com/ctllo-bit/FnDepot/master/fnpack.json", testRoot},
		{"https://raw.githubusercontent.com/ctllo-bit/FnDepot/main/fndepot.json", testRoot},
		{"https://raw.githubusercontent.com/ctllo-bit/FnDepot/main/fndepot_v2.json", testRoot},
		// 路径内 raw
		{"https://github.com/ctllo-bit/FnDepot/raw/main/fnpack.json", testRoot},
		{"https://github.com/ctllo-bit/FnDepot/raw/master/fndepot_v2.json", testRoot},
		// jsDelivr / 镜像
		{"https://cdn.jsdelivr.net/gh/ctllo-bit/FnDepot/fnpack.json", testRoot},
		{"https://gh-proxy.com/https://raw.githubusercontent.com/ctllo-bit/FnDepot/main/fnpack.json", testRoot},
		{"https://mirror.ghproxy.com/https://github.com/ctllo-bit/FnDepot/raw/main/fnpack.json", testRoot},
		// 非 GitHub
		{"https://gitea.example.com/u/r/raw/branch/fnpack.json", "gitea.example.com/u/r"},
		{"http://nas.local:8080/fndepot.json", "nas.local:8080"},
		{"http://nas.local:8080/repo/fndepot_v2.json", "nas.local:8080/repo"},
		{"https://gitea.example.com/u/r", "gitea.example.com/u/r"},
		// 非链接
		{"", ""},
		{"清理精灵", ""},
		{"moo 1.0", ""},
	}
	for _, c := range cases {
		if got := SourceURLKey(c.in); got != c.want {
			t.Errorf("SourceURLKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
