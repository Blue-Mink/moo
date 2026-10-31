package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"moo/internal/config"
)

// 0.6.379：发现页推荐位改为远程清单驱动（appname@源名，顺序即展示顺序，
// 缺卡跳过不补位）。本文件覆盖新契约；旧的「固定前三 + 快照回落 + 随机补位」
// 用例已按新设计移除（不是为了让测试变绿改产品）。

// recommendTestServer 造 Server：目录预置 + 清单由本地 httptest 提供
// （绝不触外网）。listBody 为空 → 清单 404（走内嵌兜底路径）。
func recommendTestServer(t *testing.T, catalog []AppInfo, listURL string, listBody *string) *Server {
	t.Helper()
	t.Setenv("MOO_DATA", t.TempDir())
	mux := http.NewServeMux()
	mux.HandleFunc("/recommend_list.txt", func(w http.ResponseWriter, r *http.Request) {
		if listBody == nil || *listBody == "" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(*listBody))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	u := listURL
	if u == "" {
		u = ts.URL + "/recommend_list.txt"
	}
	s := &Server{Cfg: &config.Config{RecommendListURL: u}}
	if catalog == nil {
		catalog = []AppInfo{}
	}
	s.catalogData = catalog
	s.catalogAt = time.Now()
	s.catalogTTL = time.Hour
	return s
}

func recCard(name, src string) AppInfo {
	return AppInfo{Key: name + "@" + src, AppName: name, Source: src,
		DisplayName: name, LatestVersion: "1.0.0", IconURL: "https://example.com/i.png"}
}

func recNames(apps []AppInfo) []string {
	out := make([]string, 0, len(apps))
	for _, a := range apps {
		out = append(out, a.AppName)
	}
	return out
}

// TestRecommendListOrderStable_379 顺序=清单顺序，跨请求稳定；缺卡整条跳过。
func TestRecommendListOrderStable_379(t *testing.T) {
	cat := []AppInfo{recCard("moo-mate", "Blue-Mink"), recCard("fn-knock", "kci-lnk"), recCard("demo", "tst")}
	body := "# 注释行\r\nmoo-mate@Blue-Mink\r\nfndepot@Blue-Mink\r\nfn-knock\r\n demo \r\n"
	s := recommendTestServer(t, cat, "", &body)
	for i := 0; i < 5; i++ {
		got := recNames(z5Recommended(t, s, 5))
		want := []string{"moo-mate", "fn-knock", "demo"} // fndepot 不在目录 → 跳过
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("第 %d 次：顺序=%v，期望 %v", i+1, got, want)
		}
	}
}

// TestRecommendListNoPadding_379 清单 2 条就只回 2 条（不随机补位、不留空位）。
func TestRecommendListNoPadding_379(t *testing.T) {
	cat := []AppInfo{recCard("a", "s"), recCard("b", "s"), recCard("c", "s"), recCard("d", "s")}
	body := "a@x\nb@y\n"
	s := recommendTestServer(t, cat, "", &body)
	if got := recNames(z5Recommended(t, s, 12)); fmt.Sprint(got) != "[a b]" {
		t.Fatalf("清单 2 条应只回 2 条，实际 %v", got)
	}
}

// TestRecommendListHiddenSkip_379 去重隐藏 / 用户禁用 → 整条跳过。
func TestRecommendListHiddenSkip_379(t *testing.T) {
	a, b := recCard("a", "s"), recCard("b", "s")
	a.UserHidden, b.Hidden = true, true
	cat := []AppInfo{a, b, recCard("c", "s")}
	body := "a@x\nb@y\nc\n"
	s := recommendTestServer(t, cat, "", &body)
	if got := recNames(z5Recommended(t, s, 12)); fmt.Sprint(got) != "[c]" {
		t.Fatalf("禁用/隐藏位应跳过，实际 %v", got)
	}
}

// TestRecommendListCountCaps_379 ?count 只裁剪不补足。
func TestRecommendListCountCaps_379(t *testing.T) {
	cat := []AppInfo{recCard("a", "s"), recCard("b", "s"), recCard("c", "s")}
	body := "a\nb\nc\n"
	s := recommendTestServer(t, cat, "", &body)
	if n := len(z5Recommended(t, s, 1)); n != 1 {
		t.Fatalf("count=1 应 1 条，实际 %d", n)
	}
	if n := len(z5Recommended(t, s, 12)); n != 3 {
		t.Fatalf("count=12 应 3 条（无补位），实际 %d", n)
	}
}

// TestRecommendListSourcePreferred_379 同 appname 多源：清单里的源名优先。
func TestRecommendListSourcePreferred_379(t *testing.T) {
	cat := []AppInfo{recCard("dup", "other"), recCard("dup", "tst")}
	body := "dup@tst\n"
	s := recommendTestServer(t, cat, "", &body)
	got := z5Recommended(t, s, 12)
	if len(got) != 1 || got[0].Source != "tst" {
		t.Fatalf("应选源名 tst 的卡，实际 %+v", recNames(got))
	}
}

// TestRecommendListKeepsLastGood_379 拉取失败沿用上次成功值（不清空推荐位）。
func TestRecommendListKeepsLastGood_379(t *testing.T) {
	cat := []AppInfo{recCard("a", "s"), recCard("b", "s")}
	body := "a\nb\n"
	s := recommendTestServer(t, cat, "", &body)
	if got := recNames(z5Recommended(t, s, 12)); fmt.Sprint(got) != "[a b]" {
		t.Fatalf("首次应 [a b]，实际 %v", got)
	}
	body = "" // 之后 404
	s.recoMu.Lock()
	s.recoAt = time.Time{} // 强制 TTL 过期
	s.recoMu.Unlock()
	if got := recNames(z5Recommended(t, s, 12)); fmt.Sprint(got) != "[a b]" {
		t.Fatalf("拉取失败应沿用旧值 [a b]，实际 %v", got)
	}
}

// TestRecommendListEmbeddedFallback_379 从未成功过 → 内嵌兜底 4 条。
func TestRecommendListEmbeddedFallback_379(t *testing.T) {
	// 0.6.380（用户令）：顺序纠正为 fnOS Apps → FnDepot → 敲门 → Moo Mate
	names := []string{"fnos-apps-store", "fndepot", "fn-knock", "moo-mate"}
	cat := make([]AppInfo, 0, 4)
	for _, n := range names {
		cat = append(cat, recCard(n, "s"))
	}
	s := recommendTestServer(t, cat, "", nil) // 404
	got := recNames(z5Recommended(t, s, 12))
	if fmt.Sprint(got) != fmt.Sprint(names) {
		t.Fatalf("内嵌兜底顺序应为 %v，实际 %v", names, got)
	}
}

// TestParseRecommendList_379 解析：注释/空行/行尾注释/去重/上限/坏名剔除。
func TestParseRecommendList_379(t *testing.T) {
	e := parseRecommendList("# c\n\n a@S \nb  # tail\nbad/name\nA@s\n")
	if len(e) != 2 || e[0].AppName != "a" || e[0].Source != "S" || e[1].AppName != "b" {
		t.Fatalf("解析结果异常: %+v", e)
	}
}
