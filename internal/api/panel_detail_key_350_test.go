package api

import "testing"

// 0.6.350 F1：社区源里存在与官方应用同名的条目（裸 key == appname），
// panel-detail 必须只解析官方卡，不能被精确 key 的社区卡抢先命中。
func TestOfficialCatalogByKeyPrefersOfficial(t *testing.T) {
	catalog := []AppInfo{
		{Key: "1Panel", AppName: "1Panel", Source: "Hxido-RXM"},
		{Key: "1Panel@fnos-official", AppName: "1Panel", Source: OfficialSourceID},
		{Key: "only-community@src", AppName: "only-community", Source: "src"},
		{Key: "different-key@fnos-official", AppName: "display-name", Source: OfficialSourceID},
	}
	cases := []struct {
		name    string
		key     string
		wantOK  bool
		wantKey string
	}{
		{"裸 appname 命中官方卡而非同名社区卡", "1Panel", true, "1Panel@fnos-official"},
		{"精确官方 key", "1Panel@fnos-official", true, "1Panel@fnos-official"},
		{"只有社区卡时不算官方", "only-community", false, ""},
		{"精确命中的非官方 key 被跳过", "only-community@src", false, ""},
		{"官方 key 与 appname 不同时按精确 key", "different-key@fnos-official", true, "different-key@fnos-official"},
		{"appname 后缀形态", "display-name@fnos-official", true, "different-key@fnos-official"},
		{"完全不存在的 key", "nope", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ai, ok := officialCatalogByKey(catalog, c.key)
			if ok != c.wantOK {
				t.Fatalf("ok=%v want %v (key=%q)", ok, c.wantOK, c.key)
			}
			if ok && ai.Key != c.wantKey {
				t.Fatalf("key=%q got %q want %q", c.key, ai.Key, c.wantKey)
			}
			if ok && ai.Source != OfficialSourceID {
				t.Fatalf("resolved non-official source %q", ai.Source)
			}
		})
	}
}

// 反向守卫：不加官方过滤时（旧的精确 key 优先语义）会命中社区卡——用于证明
// 上面的用例确实覆盖了 F1 场景，而不是恒真。
func TestOfficialCatalogByKeyContrastsWithPlainLookup(t *testing.T) {
	catalog := []AppInfo{
		{Key: "1Panel", AppName: "1Panel", Source: "Hxido-RXM"},
		{Key: "1Panel@fnos-official", AppName: "1Panel", Source: OfficialSourceID},
	}
	plain, ok := catalogByKeyIn(catalog, "1Panel")
	if !ok || plain.Source == OfficialSourceID {
		t.Fatalf("预期普通查表命中社区卡，实际 %+v ok=%v", plain, ok)
	}
	off, ok := officialCatalogByKey(catalog, "1Panel")
	if !ok || off.Source != OfficialSourceID {
		t.Fatalf("官方解析器应命中官方卡，实际 %+v ok=%v", off, ok)
	}
}
