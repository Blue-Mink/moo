package api

import "testing"

func TestSourceNameFromURL(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		// conversun/fnos-apps → 商店项目名 fnos-store
		{"https://github.com/conversun/fnos-apps", "fnos-store"},
		{"https://github.com/conversun/fnos-apps.git", "fnos-store"},
		{"https://github.com/conversun/fnos-apps/tree/main", "fnos-store"},
		{"https://raw.githubusercontent.com/conversun/fnos-apps/main/apps.json", "fnos-store"},
		{"https://gh-proxy.com/https://raw.githubusercontent.com/conversun/fnos-apps/main/apps.json", "fnos-store"},
		// 边界：fnos-apps-store（商店代码仓库）不是目录源，取 owner
		{"https://github.com/conversun/fnos-apps-store", "conversun"},
		// 其他 GitHub 仓库取 owner
		{"https://github.com/Blue-Mink/FnDepot", "Blue-Mink"},
		{"https://github.com/SomeAuthor/Apps.git", "SomeAuthor"},
		// 非 GitHub 取最后一段路径
		{"https://fndepot.example.com/fnpack.json", "fndepot.example.com"},
	}
	for _, c := range cases {
		if got := sourceNameFromURL(c.url); got != c.want {
			t.Errorf("sourceNameFromURL(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}
