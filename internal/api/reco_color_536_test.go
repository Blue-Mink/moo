package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// 0.6.536（用户令）：发现页推荐位横幅配色改由远程清单行尾指定。
// 本文件覆盖解析矩阵 + 端点下发；配色写成非法值时回落哈希（空串）。

func TestParseRecommendListColor_536(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{"a@S aurora-3", "aurora-3"},
		{"b aurora12", "aurora-12"},
		{"c@S 7", "aurora-7"},
		{"d@S@aurora-12", "aurora-12"},
		{"e@S aurora-13", ""},  // 越界 → 不认（不丢整条）
		{"f@S # aurora-3", ""}, // # 后是注释
		{"g@S 行尾注释", ""},       // 旧写法：行尾注释被忽略
		{"h@S 0", ""},          // 0 非法
		{"i@S", ""},            // 未写色
	}
	for _, c := range cases {
		e := parseRecommendList(c.line)
		if len(e) != 1 {
			t.Fatalf("%q 应解析出 1 条，实际 %d", c.line, len(e))
		}
		if e[0].Color != c.want {
			t.Fatalf("%q 配色应为 %q，实际 %q", c.line, c.want, e[0].Color)
		}
	}
}

// TestRecommendedCarriesColor_536 清单里的配色随卡片下发（未写色 = 空）。
func TestRecommendedCarriesColor_536(t *testing.T) {
	cat := []AppInfo{recCard("a", "s"), recCard("b", "s")}
	body := "a@s aurora-11\nb@s\n"
	s := recommendTestServer(t, cat, "", &body)
	got := z5Recommended(t, s, 12)
	if len(got) != 2 {
		t.Fatalf("应回 2 条，实际 %d", len(got))
	}
	if got[0].RecoColor != "aurora-11" {
		t.Fatalf("首条配色应为 aurora-11，实际 %q", got[0].RecoColor)
	}
	if got[1].RecoColor != "" {
		t.Fatalf("未写色应为空，实际 %q", got[1].RecoColor)
	}
}

// TestRecommendedRefreshColors_536 管理员刷新接口一并回显配色（便于核对清单）。
func TestRecommendedRefreshColors_536(t *testing.T) {
	cat := []AppInfo{recCard("a", "s")}
	body := "a@s aurora-2\n"
	s := recommendTestServer(t, cat, "", &body)
	rec := httptest.NewRecorder()
	s.recommendedRefresh(rec, httptest.NewRequest(http.MethodPost, "/api/recommended/refresh", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh 应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Colors []string `json:"colors"`
		Count  int      `json:"count"`
	}
	if err := jsonUnmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	if resp.Count != 1 || len(resp.Colors) != 1 || resp.Colors[0] != "aurora-2" {
		t.Fatalf("colors 回显异常: %s", rec.Body.String())
	}
}
