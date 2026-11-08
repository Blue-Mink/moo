package api

import (
	"testing"

	"moo/internal/config"
	"moo/internal/source"
)

// 0.6.521：多条自定义加速源 —— 选项注入顺序与 key 约定。
func TestCustomMirrorOptionsInjected521(t *testing.T) {
	config.SetCustomMirrors(
		[]string{"https://gh1.example/", "https://gh2.example/"},
		[]string{"https://dk1.example/"},
	)
	t.Cleanup(func() { config.SetCustomMirrors(nil, nil) })

	gh := config.GitHubMirrorOptions()
	if got := config.MirrorURL(gh, "custom"); got != "https://gh1.example/" {
		t.Fatalf("custom URL = %q", got)
	}
	if got := config.MirrorURL(gh, "custom-2"); got != "https://gh2.example/" {
		t.Fatalf("custom-2 URL = %q", got)
	}
	foundLabel := false
	for _, o := range gh {
		if o.Key == "custom-2" && o.Label == "自定义 2" {
			foundLabel = true
		}
	}
	if !foundLabel {
		t.Fatal("custom-2 应带「自定义 2」标签")
	}
	dk := config.DockerMirrorOptions()
	if got := config.MirrorURL(dk, "custom"); got != "https://dk1.example/" {
		t.Fatalf("docker custom URL = %q", got)
	}
}

// 0.6.521：设置页一次提交多条 → 数组落库、单值同步第一条、空数组清空。
func TestPutSettingsCustomMirrorList521(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	t.Cleanup(func() { config.SetCustomMirrors(nil, nil) })
	cfg := &config.Config{Mirror: "auto", DockerMirror: "auto"}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg)}

	putSettingsBody(t, s, `{"custom_github_mirrors":["https://gh1.example/","https://gh2.example/"],"custom_docker_mirrors":["https://dk1.example/"]}`)
	if len(cfg.CustomGitHubMirrors) != 2 || cfg.CustomGitHubMirror != "https://gh1.example/" {
		t.Fatalf("github list=%v single=%q", cfg.CustomGitHubMirrors, cfg.CustomGitHubMirror)
	}
	if len(cfg.CustomDockerMirrors) != 1 || cfg.CustomDockerMirror != "https://dk1.example/" {
		t.Fatalf("docker list=%v single=%q", cfg.CustomDockerMirrors, cfg.CustomDockerMirror)
	}

	putSettingsBody(t, s, `{"custom_github_mirrors":[]}`)
	if len(cfg.CustomGitHubMirrors) != 0 || cfg.CustomGitHubMirror != "" {
		t.Fatalf("空数组应清空: %v / %q", cfg.CustomGitHubMirrors, cfg.CustomGitHubMirror)
	}

	putSettingsBodyExpect400(t, s, `{"custom_docker_mirrors":["ftp://bad.example/"]}`)
}
