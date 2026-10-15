package api

// 0.6.325 安全加固 R1 单测：「源列表同步」驱动的加源先过 netguard.Public。
//
// 背景：清单内容由远端（内置列表地址 / 管理员自建列表）决定，若被篡改塞入
// 内网地址，一旦入库该主机 ≤30s 即进 netguard 豁免表 → 成为 SSRF 跳板。
// 现默认跳过非公共地址（开关 SourceListAllowPrivate 可显式放开）。
//
// 注意：本包 TestMain 已把 127.0.0.1/localhost 放进豁免表（模拟「管理员显式
// 配置的主机」），故清单自身所在主机照常放行；用例中的 127.0.0.2 未被豁免，
// 代表「清单里的其它内网地址」。

import (
	"strings"
	"testing"

	"moo/internal/config"
	"moo/internal/source"
)

// R1-a 默认（开关关）：清单里的非公共地址被跳过并附「跳过（安全策略）」原因，
// 且绝不入 cfg.Sources；同清单里已豁免的主机照常添加。
func TestAddMissingSourcesSkipsNonPublicByDefault(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	srv := newListSyncServer(t, func(b string) string {
		return b + "/any\n" + "http://127.0.0.2:1/internal\n"
	}, nil)
	cfg := &config.Config{SourceListURL: srv.URL + "/list"}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg)}

	if cfg.SourceListAllowPrivate {
		t.Fatal("SourceListAllowPrivate 默认必须为 false")
	}
	res, err := s.sourceListSyncOnce(t.Context())
	if err != nil {
		t.Fatalf("抓取清单失败: %v", err)
	}
	if res.Added != 1 || res.Failed != 1 {
		t.Fatalf("期望 added=1 failed=1, got added=%d failed=%d errs=%v", res.Added, res.Failed, res.Errors)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "跳过（安全策略）") {
		t.Fatalf("跳过原因应含「跳过（安全策略）」，got %v", res.Errors)
	}
	if len(cfg.Sources) != 1 {
		t.Fatalf("非公共地址不应入库, sources=%d (%+v)", len(cfg.Sources), cfg.Sources)
	}
	for _, sr := range cfg.Sources {
		if strings.Contains(sr.URL, "127.0.0.2") {
			t.Fatalf("非公共地址混入 cfg.Sources: %+v", sr)
		}
	}
}

// R1-b 开关开（SourceListAllowPrivate=true）：同一条非公共地址不再被安全策略
// 拦截（改为按正常流程尝试；此处因不可达而失败）——证明确实只有一处拦截点，
// 且开关真实生效。
func TestAddMissingSourcesAllowsPrivateWhenEnabled(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	srv := newListSyncServer(t, func(b string) string {
		return b + "/any\n" + "http://127.0.0.2:1/internal\n"
	}, nil)
	cfg := &config.Config{SourceListURL: srv.URL + "/list", SourceListAllowPrivate: true}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg)}

	res, err := s.sourceListSyncOnce(t.Context())
	if err != nil {
		t.Fatalf("抓取清单失败: %v", err)
	}
	if res.Added != 1 {
		t.Fatalf("期望 added=1（豁免主机那条）, got added=%d errs=%v", res.Added, res.Errors)
	}
	for _, e := range res.Errors {
		if strings.Contains(e, "跳过（安全策略）") {
			t.Fatalf("开关开时不应再出现安全策略跳过: %v", e)
		}
	}
	if res.Failed != 1 {
		t.Fatalf("非公共地址应走正常流程并因不可达失败, got failed=%d errs=%v", res.Failed, res.Errors)
	}
}

// R1-c 设置往返：PUT 指针语义（nil=不改动 / true 落盘 / 显式 false 关回去），
// 与 install_volume、check_interval_hours 同款契约。
func TestPutSettingsSourceListAllowPrivate(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg)}

	if cfg.SourceListAllowPrivate {
		t.Fatal("默认应为 false")
	}
	putSettingsBody(t, s, `{"source_list_allow_private": true}`)
	if !cfg.SourceListAllowPrivate {
		t.Fatal("PUT true 应落盘")
	}
	putSettingsBody(t, s, `{"check_interval_hours": 6}`)
	if !cfg.SourceListAllowPrivate {
		t.Fatal("未提交 source_list_allow_private 不应改动")
	}
	putSettingsBody(t, s, `{"source_list_allow_private": false}`)
	if cfg.SourceListAllowPrivate {
		t.Fatal("PUT false 应关回去（指针区分未提交与显式 false）")
	}
}
