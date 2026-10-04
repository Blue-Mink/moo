package lang

import (
	"context"
	"testing"
)

func TestFromDefault(t *testing.T) {
	if got := From(context.Background()); got != "zh-CN" {
		t.Errorf("From(empty ctx) = %q, want zh-CN", got)
	}
	if got := From(WithContext(context.Background(), "")); got != "zh-CN" {
		t.Errorf("With empty string should keep default, got %q", got)
	}
}

func TestWithContext(t *testing.T) {
	ctx := WithContext(context.Background(), "en-US")
	if got := From(ctx); got != "en-US" {
		t.Errorf("From = %q, want en-US", got)
	}
}

func TestFromAcceptLanguage(t *testing.T) {
	cases := []struct {
		header string
		want   string
	}{
		{"", "zh-CN"},
		{"zh-CN", "zh-CN"},
		{"zh", "zh-CN"},
		{"zh-TW,zh;q=0.9", "zh-CN"}, // 面板仅 zh-CN/en-US 可用，zh-TW 归 zh-CN
		{"en-US,en;q=0.9", "en-US"},
		{"en-GB", "en-US"},
		{"fr-FR,fr;q=0.9", "zh-CN"}, // 不支持的语言回退默认
		{"en;q=0.9, zh;q=0.8", "en-US"},
	}
	for _, c := range cases {
		if got := FromAcceptLanguage(c.header); got != c.want {
			t.Errorf("FromAcceptLanguage(%q) = %q, want %q", c.header, got, c.want)
		}
	}
}

func TestSupported(t *testing.T) {
	for _, v := range []string{"auto", "zh-CN", "en-US"} {
		if !Supported[v] {
			t.Errorf("Supported[%q] = false, want true", v)
		}
	}
	if Supported["fr-FR"] {
		t.Error("Supported[fr-FR] = true, want false")
	}
}
