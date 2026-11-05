package api

// 2026-11-04（PR #14 纯收益部分）：单值自定义加速地址的保存校验。

import (
	"testing"

	"moo/internal/config"
	"moo/internal/source"
)

func TestPutSettingsCustomGitHubMirrorValidation(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg)}

	// 形状不合法一律 400：漏协议 / 非 http(s) / 带凭据 / 过长
	for _, body := range []string{
		`{"custom_github_mirror": "gh.example.com/"}`,
		`{"custom_github_mirror": "ftp://gh.example.com/"}`,
		`{"custom_github_mirror": "https://user:pass@gh.example.com/"}`,
		`{"custom_github_mirror": "https://gh.example.com/` + longPath() + `"}`,
	} {
		putSettingsBodyExpect400(t, s, body)
		if cfg.CustomGitHubMirror != "" {
			t.Fatalf("非法地址不应落盘: %q（body=%s）", cfg.CustomGitHubMirror, body)
		}
	}

	// 合法地址：去首尾空格后保存（局域网自建代理仍允许 http / 内网 IP）
	putSettingsBody(t, s, `{"custom_github_mirror": "  https://gh.example.com/  "}`)
	if cfg.CustomGitHubMirror != "https://gh.example.com/" {
		t.Fatalf("合法地址应去空格保存: %q", cfg.CustomGitHubMirror)
	}
	putSettingsBody(t, s, `{"custom_github_mirror": "http://192.0.2.10:8080/"}`)
	if cfg.CustomGitHubMirror != "http://192.0.2.10:8080/" {
		t.Fatalf("局域网自建代理应允许: %q", cfg.CustomGitHubMirror)
	}

	// 空串 = 显式清除（既有语义不能破坏）
	putSettingsBody(t, s, `{"custom_github_mirror": ""}`)
	if cfg.CustomGitHubMirror != "" {
		t.Fatalf("空串应清除: %q", cfg.CustomGitHubMirror)
	}
}

func longPath() string {
	b := make([]byte, 2100)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}
