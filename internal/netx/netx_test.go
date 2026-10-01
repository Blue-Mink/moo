package netx

import (
	"testing"
)

func TestIsGitHubHost(t *testing.T) {
	yes := []string{
		"github.com",
		"raw.githubusercontent.com",
		"api.github.com",
		"object.githubusercontent.com",
		"codeload.github.com",
		"camo.githubusercontent.com",
		"githubassets.com",
		"github.dev",
		"GITHUB.COM", // 大小写不敏感
		"github.com.", // 尾点
	}
	for _, h := range yes {
		if !isGitHubHost(h) {
			t.Errorf("isGitHubHost(%q) = false, want true", h)
		}
	}
	no := []string{
		"gh-proxy.com",
		"jsdelivr.com",
		"fastly.com",
		"evilevil.github.com.evil.example", // 后缀伪装
		"notgithub.com",
		"github.com.evil.example",
		"localhost",
		"127.0.0.1",
	}
	for _, h := range no {
		if isGitHubHost(h) {
			t.Errorf("isGitHubHost(%q) = true, want false", h)
		}
	}
}

func TestValidate(t *testing.T) {
	valid := []string{
		"http://127.0.0.1:7890",
		"https://proxy.example:8080",
		"socks5://127.0.0.1:1080",
		"socks5h://127.0.0.1:1080",
		"socks4://192.0.2.1:1080",
	}
	for _, u := range valid {
		if err := Validate(u); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", u, err)
		}
	}
	invalid := []string{
		"",
		"  ",
		"ftp://x:1",
		"http://",
		"::://bad",
	}
	for _, u := range invalid {
		if err := Validate(u); err == nil {
			t.Errorf("Validate(%q) = nil, want error", u)
		}
	}
}
