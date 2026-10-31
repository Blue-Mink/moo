package main

import (
	"net/http"
	"net/url"
	"testing"
)

// 只有 GitHub 系域名走本机加速，其它站点必须直连。
func TestIsMateAccelHost(t *testing.T) {
	yes := []string{
		"github.com", "api.github.com", "raw.githubusercontent.com",
		"objects.githubusercontent.com", "user-images.githubusercontent.com",
		"release-assets.githubusercontent.com", "USER-IMAGES.GITHUBUSERCONTENT.COM",
	}
	for _, h := range yes {
		if !isMateAccelHost(h) {
			t.Fatalf("%q 应走加速", h)
		}
	}
	no := []string{"", "gitee.com", "fndepot.example.com", "127.0.0.1", "mygithub.com.example.org"}
	for _, h := range no {
		if isMateAccelHost(h) {
			t.Fatalf("%q 不该走加速", h)
		}
	}
}

// 代理选择：GitHub 走本机代理，非 GitHub 与回环地址一律直连。
func TestMateProxyFunc(t *testing.T) {
	orig := accelProxyProvider
	defer func() { accelProxyProvider = orig }()
	accelProxyProvider = func() *url.URL { return &url.URL{Scheme: "http", Host: "127.0.0.1:37710"} }

	cases := []struct {
		target  string
		wantPxy bool
	}{
		{"https://github.com/Blue-Mink/moo", true},
		{"https://raw.githubusercontent.com/a/b/main/moo.json", true},
		{"https://fndepot.example.com/fnpack.json", false},
		{"http://127.0.0.1:38100/api/about", false},
		{"http://localhost:38100/api/about", false},
	}
	for _, c := range cases {
		req, err := http.NewRequest(http.MethodGet, c.target, nil)
		if err != nil {
			t.Fatalf("构造请求失败: %v", err)
		}
		got, err := mateProxyFunc(req)
		if err != nil {
			t.Fatalf("mateProxyFunc 返回错误: %v", err)
		}
		if (got != nil) != c.wantPxy {
			t.Fatalf("%s 代理期望 %v，实际 %v", c.target, c.wantPxy, got)
		}
	}

	// Dog-dev 不可用时（provider 返回 nil）不能阻断请求，必须回退直连。
	accelProxyProvider = func() *url.URL { return nil }
	req, _ := http.NewRequest(http.MethodGet, "https://github.com/x/y", nil)
	if got, _ := mateProxyFunc(req); got != nil {
		t.Fatalf("Dog-dev 不可用时应直连，实际 %v", got)
	}
}

// 逃生门：设置 MOO_MATE_NO_ACCEL=1 后即使本机加速可用也走直连。
func TestMateProxyFuncDisabledByEnv(t *testing.T) {
	orig := accelProxyProvider
	defer func() { accelProxyProvider = orig }()
	accelProxyProvider = func() *url.URL { return &url.URL{Scheme: "http", Host: "127.0.0.1:37710"} }

	t.Setenv("MOO_MATE_NO_ACCEL", "1")
	req, _ := http.NewRequest(http.MethodGet, "https://github.com/x/y", nil)
	if got, _ := mateProxyFunc(req); got != nil {
		t.Fatalf("开关打开后不该走代理，实际 %v", got)
	}
}
