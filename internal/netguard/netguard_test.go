package netguard

import (
	"strings"
	"testing"
)

func TestPublicRejectsInternal(t *testing.T) {
	SetRebuilder(nil)
	cases := []string{
		"http://127.0.0.1:5666/",            // 环回
		"http://172.16.0.5/admin",           // 私网
		"http://172.16.0.1/",               // 私网
		"http://172.16.0.10/",               // 私网
		"http://169.254.169.254/latest/",    // 链路本地/元数据
		"http://[::1]/",                     // IPv6 环回
		"http://localhost/x",                // 环回别名
		"ftp://github.com/a/b",              // 非 http(s)
		"--exec=whoami",                     // aria2 选项注入形态
		"file:///etc/passwd",                // 文件协议
		"http:///path",                      // 缺主机
	}
	for _, u := range cases {
		if _, err := Public(u); err == nil {
			t.Errorf("Public(%q) 应拒绝", u)
		}
	}
}

func TestPublicAllowsPublic(t *testing.T) {
	SetRebuilder(nil)
	if _, err := Public("https://raw.githubusercontent.com/o/r/main/f.txt"); err != nil {
		t.Fatalf("公共 raw 地址应放行: %v", err)
	}
	if _, err := Public("https://cdn.jsdelivr.net/gh/o/r@main/f"); err != nil {
		t.Fatalf("公共 CDN 地址应放行: %v", err)
	}
}

func TestExemptAllowsPrivate(t *testing.T) {
	SetRebuilder(func() []string {
		return []string{"172.16.0.99"}
	})
	// 豁免主机直接放行（管理员自建镜像/源场景）
	if _, err := Public("http://172.16.0.99:8080/x"); err != nil {
		t.Fatalf("豁免私网地址应放行: %v", err)
	}
	// 其它私网地址仍拒绝
	if _, err := Public("http://172.16.0.2/admin"); err == nil {
		t.Fatal("非豁免私网地址应拒绝")
	}
}

func TestIsLoopback(t *testing.T) {
	ok := []string{"127.0.0.1", "127.5.6.7", "localhost", "::1", "LOCALHOST"}
	for _, h := range ok {
		if !IsLoopback(h) {
			t.Errorf("IsLoopback(%q) 应为 true", h)
		}
	}
	bad := []string{"172.16.0.1", "8.8.8.8", "example.com", ""}
	for _, h := range bad {
		if IsLoopback(h) {
			t.Errorf("IsLoopback(%q) 应为 false", h)
		}
	}
}

func TestPublicSchemeGuard(t *testing.T) {
	SetRebuilder(nil)
	for _, u := range []string{
		"-x16",
		"--exec=touch /tmp/pwned",
		"javascript:alert(1)",
	} {
		if _, err := Public(u); err == nil {
			t.Errorf("Public(%q) 应拒绝（aria2/协议注入形态）", u)
		}
	}
	// 正常 URL 不因 guard 误伤（解析层）
	if h, err := Public("http://github.com/o/r"); err != nil || !strings.Contains(h, "github.com") {
		t.Errorf("正常 URL 应通过且返回主机, got %q %v", h, err)
	}
}
