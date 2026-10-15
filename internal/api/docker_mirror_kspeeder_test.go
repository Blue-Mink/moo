package api

// 0.6.325 G1（BUG-B1）：Docker 下拉手选「KSpeeder (本地)」点应用必 400。
// 本测只验地址解析函数，不触碰 /etc/docker/daemon.json。

import (
	"testing"

	"moo/internal/config"
)

func TestDockerMirrorStaticURL(t *testing.T) {
	cases := []struct {
		name   string
		mirror string
		want   string
	}{
		{"kspeeder 映射本地代理", "kspeeder", "https://" + config.KSpeederLocalAddr + "/"},
		{"有 URL 的源原样返回", "daocloud", "https://m.daocloud.io/"},
		{"不存在的源返回空", "no-such-mirror", ""},
		{"custom 无 URL 返回空（由 custom 分支处理）", "custom", ""},
		{"direct 无 URL 返回空（由 direct 分支处理）", "direct", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := dockerMirrorStaticURL(c.mirror); got != c.want {
				t.Fatalf("dockerMirrorStaticURL(%q) = %q, want %q", c.mirror, got, c.want)
			}
		})
	}
}
