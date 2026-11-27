package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aead.dev/minisign"

	"moo/internal/config"
)

// 测试用 minisign 密钥对：清单已强制验签（批次③），逐例生成避免依赖生产私钥。
var testBLPub, testBLPriv = func() (minisign.PublicKey, minisign.PrivateKey) {
	pk, sk, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return pk, sk
}()

// useTestBlacklistKey 把生产内嵌公钥临时换成测试公钥（用例结束自动还原）。
func useTestBlacklistKey(t *testing.T) {
	t.Helper()
	blacklistVerifyKeyMu.Lock()
	old := blacklistVerifyKey
	blacklistVerifyKey = testBLPub
	blacklistVerifyKeyMu.Unlock()
	t.Cleanup(func() {
		blacklistVerifyKeyMu.Lock()
		blacklistVerifyKey = old
		blacklistVerifyKeyMu.Unlock()
	})
}

// 2026-11-22 远端下架黑名单单测：匹配粒度 / 目录闸 / 拉取失败语义（
// last-known-good 与 fail-open）/ 动作拦截文案。全部 httptest，不触外网。

// blListServer 可切换内容的黑名单清单源（base 路径 = <URL>/lists）。
type blListServer struct {
	srv  *httptest.Server
	mu   sync.Mutex
	body string
	code int
	sig  string
	hits int64 // 收到的请求数（含 .minisig），用于退避/频次断言
}

// hitCount 累计请求数（原子读）。
func (b *blListServer) hitCount() int64 { return atomic.LoadInt64(&b.hits) }

func (b *blListServer) set(body string, code int) {
	b.mu.Lock()
	b.body, b.code = body, code
	if code == http.StatusOK && body != "" {
		b.sig = string(minisign.Sign(testBLPriv, []byte(body)))
	}
	b.mu.Unlock()
}

// setUnsigned 只改正文、保留旧签名 = 模拟「正文被篡改，签名没跟上」。
func (b *blListServer) setUnsigned(body string) {
	b.mu.Lock()
	b.body = body
	b.mu.Unlock()
}

// setSig 直接改签名（空串 = 缺签名）。
func (b *blListServer) setSig(sig string) {
	b.mu.Lock()
	b.sig = sig
	b.mu.Unlock()
}

func (b *blListServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&b.hits, 1)
		b.mu.Lock()
		body, code := b.body, b.code
		b.mu.Unlock()
		if code != http.StatusOK {
			w.WriteHeader(code)
			return
		}
		_, _ = w.Write([]byte(body))
	}
}

func (b *blListServer) sigHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&b.hits, 1)
		b.mu.Lock()
		sig, code := b.sig, b.code
		b.mu.Unlock()
		if code != http.StatusOK {
			w.WriteHeader(code)
			return
		}
		_, _ = w.Write([]byte(sig))
	}
}

func newBlacklistEnv(t *testing.T, body string, code int) (*Server, *blListServer) {
	t.Helper()
	mux := http.NewServeMux()
	useTestBlacklistKey(t)
	b := &blListServer{body: body, code: code}
	b.set(body, code)
	h := b.handler()
	sigH := b.sigHandler()
	mux.HandleFunc("/lists/blacklist.json", h)
	mux.HandleFunc("/lists/blacklist.json.minisig", sigH)
	mux.HandleFunc("/blacklist.json", h)
	mux.HandleFunc("/blacklist.json.minisig", sigH)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	// SourceListAllowPrivate：httptest 是 127.0.0.1，netguard 默认拒绝内网。
	// 同时把根目录兼容副本也指到本地：否则第二个候选会真的去打 GitHub。
	oldLegacy := blacklistLegacy()
	setBlacklistLegacyURL(ts.URL + "/blacklist.json")
	t.Cleanup(func() { setBlacklistLegacyURL(oldLegacy) })
	s := &Server{Cfg: &config.Config{ListBaseURL: ts.URL + "/lists", SourceListAllowPrivate: true}}
	// 每个用例独立的 LKG 快照路径：否则默认 ./moo-data 会被写脏，
	// 让「从未拉取 = fail-open」用例读到别人留下的快照而误判。
	s.BlacklistLKGPath = filepath.Join(t.TempDir(), "blacklist_last.json")
	return s, b
}

// injectBlacklist 直接注入生效清单（跳过网络），让匹配/闸的用例确定且快。
func injectBlacklist(s *Server, entries []blacklistEntry) {
	st := s.blacklistState()
	st.mu.Lock()
	st.entries = normalizeBlacklist(entries)
	st.loaded = true
	st.at = time.Now()
	st.mu.Unlock()
}

const blPayload = `{"schema_version":1,"updated_at":"2026-11-22","entries":[
 {"app":"BadApp","source":"*","reason":"malware","since":"2026-11-22"},
 {"app":"srcapp","source":"某源","reason":"policy"},
 {"app":"goneapp","source":"*","reason":"policy","until":"2020-01-01"},
 {"app":"legacyapp","source":"*","reason":"fraud","until":"2099-01-01"}
]}`

// TestBlacklistMatchRules 全源命中 / 指定源命中 / 其它源同名不命中 / until 过期不命中。
func TestBlacklistMatchRules(t *testing.T) {
	entries, err := parseBlacklist(blPayload)
	if err != nil {
		t.Fatalf("parseBlacklist: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("应解析出 4 条，实际 %d", len(entries))
	}
	now := time.Now()
	cases := []struct {
		name   string
		app    string
		source string
		want   bool
	}{
		{"全源命中（大小写不敏感）", "badapp", "任意源", true},
		{"全源命中（无源名）", "BADAPP", "", true},
		{"指定源命中", "srcapp", "某源", true},
		{"其它源同名不命中", "srcapp", "别的源", false},
		{"until 过期不命中", "goneapp", "任意源", false},
		{"未过期仍命中", "legacyapp", "任意源", true},
	}
	for _, c := range cases {
		_, got := blacklistHitIn(entries, c.app, c.source, "", now)
		if got != c.want {
			t.Errorf("%s：app=%q source=%q want=%v got=%v", c.name, c.app, c.source, c.want, got)
		}
	}
}

// TestBlacklistAnnotateKeepsAll 目录打标（P1-4 定稿）：命中卡**全部保留**
// （已装 + 未装都不删）并写字段；同源其它应用不受影响；指定源条目只打自己
// 那张卡；空清单 fail-open 时一张都不打标。
func TestBlacklistAnnotateKeepsAll(t *testing.T) {
	in := []AppInfo{
		{Key: "badapp@A", AppName: "badapp", Source: "A", Installed: false},
		{Key: "goodapp@A", AppName: "goodapp", Source: "A", Installed: false},
		{Key: "badapp@B", AppName: "badapp", Source: "B", Installed: false},
		{Key: "badapp", AppName: "badapp", Source: "A", Installed: true},
	}
	marks := func(list []AppInfo) map[string]AppInfo {
		m := make(map[string]AppInfo, len(list))
		for _, a := range list {
			m[a.Key] = a
		}
		return m
	}

	// ① 全源条目：4 张卡全在（含已装），两张 badapp 打标（已装/未装一致），
	//    goodapp 不动；reason/notice/since 逐字段透出。
	s := &Server{Cfg: &config.Config{}}
	injectBlacklist(s, []blacklistEntry{{
		App: "badapp", Source: "*", Reason: "malware",
		Since: "2026-11-20", Notice: "内置脚本会执行未知程序",
	}})
	got := s.annotateBlacklisted(in)
	if len(got) != len(in) {
		t.Fatalf("命中卡不得从目录删除（应保留全部 %d 张），实际 %d 张: %+v", len(in), len(got), got)
	}
	m := marks(got)
	for _, k := range []string{"badapp@A", "badapp"} {
		a, ok := m[k]
		if !ok {
			t.Fatalf("命中卡 %s 被删了（用户定稿：保留并打标）", k)
		}
		if !a.Blacklisted || a.BlacklistReason != "malware" ||
			a.BlacklistNotice != "内置脚本会执行未知程序" || a.BlacklistSince != "2026-11-20" {
			t.Fatalf("%s 打标字段不对: %+v", k, a)
		}
	}
	if g := m["goodapp@A"]; g.Blacklisted || g.BlacklistReason != "" {
		t.Fatalf("未命中卡不得被打标: %+v", g)
	}
	// 有命中时走「延迟拷贝」：改的是副本，入参切片元素保持干净（否则
	// 缓存目录被多处共享时会串味）。
	if in[0].Blacklisted || in[3].Blacklisted {
		t.Fatalf("annotateBlacklisted 不得就地改写入参: %+v", in)
	}

	// ② 指定源条目：只打 A 源那张，B 源同名卡必须干净。
	s2 := &Server{Cfg: &config.Config{}}
	injectBlacklist(s2, []blacklistEntry{{App: "badapp", Source: "A", Reason: "copyright"}})
	m2 := marks(s2.annotateBlacklisted(in))
	if a := m2["badapp@A"]; !a.Blacklisted || a.BlacklistReason != "copyright" {
		t.Fatalf("A 源 badapp 应打标 copyright，实际 %+v", a)
	}
	if a := m2["badapp"]; !a.Blacklisted {
		t.Fatalf("已装 badapp 来自 A 源，同样应打标: %+v", a)
	}
	if a := m2["badapp@B"]; a.Blacklisted {
		t.Fatalf("其它源同名卡不得被连坐: %+v", a)
	}

	// ③ 空清单：原样返回（fail-open 时不打任何标）。
	s3 := &Server{Cfg: &config.Config{}}
	injectBlacklist(s3, nil)
	got3 := s3.annotateBlacklisted(in)
	if len(got3) != len(in) {
		t.Fatalf("空清单不得改动卡片数，实际 %d/%d", len(got3), len(in))
	}
	for _, a := range got3 {
		if a.Blacklisted {
			t.Fatalf("空清单不得打标: %+v", a)
		}
	}
}

// TestBlacklistAnnotateClearsUpdateFlags 命中卡必须清掉「有更新」类标记：
// 下架卡不能更新，留着角标只会把用户引到必被动作闸拦截的按钮上。
func TestBlacklistAnnotateClearsUpdateFlags(t *testing.T) {
	s := &Server{Cfg: &config.Config{}}
	injectBlacklist(s, []blacklistEntry{{App: "badapp", Reason: "malware"}})
	in := []AppInfo{
		{Key: "badapp@A", AppName: "badapp", Source: "A", Installed: true,
			HasUpdate: true, UpdateIgnored: true, UpdateIgnoredPending: true,
			AvailableVersion: "9.9.9"},
		{Key: "goodapp@A", AppName: "goodapp", Source: "A", HasUpdate: true},
	}
	got := s.annotateBlacklisted(in)
	if len(got) != 2 || !got[0].Blacklisted {
		t.Fatalf("命中卡应保留并打标，实际 %+v", got)
	}
	if got[0].HasUpdate || got[0].UpdateIgnored || got[0].UpdateIgnoredPending {
		t.Fatalf("命中卡的有更新标记必须清空: %+v", got[0])
	}
	if got[0].AvailableVersion != "9.9.9" {
		t.Fatalf("available_version 保留（详情仍可展示），实际 %q", got[0].AvailableVersion)
	}
	if !got[1].HasUpdate || got[1].Blacklisted {
		t.Fatalf("未命中卡的有更新标记不得被清: %+v", got[1])
	}
}

// TestBlacklistNoticeNormalize notice：trim、按 **rune** 截断到 60、超长不丢条目。
func TestBlacklistNoticeNormalize(t *testing.T) {
	long := strings.Repeat("恶", 61)
	entries := normalizeBlacklist([]blacklistEntry{
		{App: "ok", Notice: "  内置脚本会执行未知程序  "},
		{App: "long", Notice: long},
		{App: "empty", Notice: "   "},
	})
	if len(entries) != 3 {
		t.Fatalf("notice 超长不得丢条目（应 3 条），实际 %d: %+v", len(entries), entries)
	}
	if got := entries[0].Notice; got != "内置脚本会执行未知程序" {
		t.Fatalf("notice 应去首尾空白，实际 %q", got)
	}
	if n := len([]rune(entries[1].Notice)); n != blacklistNoticeMaxRunes {
		t.Fatalf("notice 应按 rune 截断到 %d，实际 %d 字", blacklistNoticeMaxRunes, n)
	}
	if !strings.HasPrefix(long, entries[1].Notice) {
		t.Fatal("截断必须是原文前缀（不得切出半个字符）")
	}
	if entries[2].Notice != "" {
		t.Fatalf("纯空白 notice 应归一化为空，实际 %q", entries[2].Notice)
	}
	// ASCII 同样按 rune（字节）算：61 个 a → 60 个 a。
	ascii := normalizeBlacklist([]blacklistEntry{{App: "ascii", Notice: strings.Repeat("a", 61)}})
	if len(ascii[0].Notice) != blacklistNoticeMaxRunes {
		t.Fatalf("ASCII notice 也应截断到 %d，实际 %d", blacklistNoticeMaxRunes, len(ascii[0].Notice))
	}
	if blacklistNoticeMaxRunes != 60 {
		t.Fatalf("DESIGN §0 定稿的 notice 上限必须是 60 字，实际 %d", blacklistNoticeMaxRunes)
	}
}

// TestBlacklistHitBestSpecificity 最具体优先：指纹+源 > 指纹 > 应用+源 > 应用；
// 同级取清单靠前的一条；过期条目不参与挑选。
func TestBlacklistHitBestSpecificity(t *testing.T) {
	now := time.Now()
	appOnly := blacklistEntry{App: "appx", Source: "*", Reason: "policy", Notice: "泛化"}
	appSrc := blacklistEntry{App: "appx", Source: "A", Reason: "fraud", Notice: "应用+源"}
	shaOnly := blacklistEntry{App: "appx", Source: "*", Sha256: blShaA, Reason: "security", Notice: "指纹"}
	shaSrc := blacklistEntry{App: "appx", Source: "A", Sha256: blShaA, Reason: "malware", Notice: "指纹+源"}
	// 清单把泛化条目排最前：仍须取最具体那条（不是「先命中先用」）。
	entries := []blacklistEntry{appOnly, appSrc, shaOnly, shaSrc}

	if e, ok := blacklistHitBest(entries, "appx", "A", blShaA, now); !ok || e.Notice != "指纹+源" {
		t.Fatalf("指纹+源命中时应取它，实际 %+v ok=%v", e, ok)
	}
	if e, ok := blacklistHitBest(entries, "appx", "A", "", now); !ok || e.Notice != "应用+源" {
		t.Fatalf("指纹未知时应落到应用+源，实际 %+v ok=%v", e, ok)
	}
	if e, ok := blacklistHitBest(entries, "appx", "", "", now); !ok || e.Notice != "泛化" {
		t.Fatalf("只有应用名时只能取应用级条目，实际 %+v ok=%v", e, ok)
	}

	// 同级取清单靠前。
	tieA := blacklistEntry{App: "tie", Source: "*", Reason: "policy", Notice: "先"}
	tieB := blacklistEntry{App: "tie", Source: "*", Reason: "malware", Notice: "后"}
	if e, ok := blacklistHitBest([]blacklistEntry{tieA, tieB}, "tie", "任意源", "", now); !ok || e.Notice != "先" {
		t.Fatalf("同级应取清单靠前的一条，实际 %+v ok=%v", e, ok)
	}

	// 过期（until 已过）条目不得参与挑选。
	expired := blacklistEntry{App: "old", Source: "*", Sha256: blShaB, Reason: "malware", Notice: "过期", Until: "2020-01-01"}
	live := blacklistEntry{App: "old", Source: "*", Reason: "policy", Notice: "生效"}
	if e, ok := blacklistHitBest(normalizeBlacklist([]blacklistEntry{expired, live}), "old", "", blShaB, now); !ok || e.Notice != "生效" {
		t.Fatalf("过期条目不得参与挑选，实际 %+v ok=%v", e, ok)
	}

	if _, ok := blacklistHitBest(entries, "nope", "A", blShaA, now); ok {
		t.Fatal("不命中必须回 false")
	}
}

// TestBlacklistReasonShort 卡面短句（DESIGN §2 定稿）：六个原因码 + 空/未知回落。
func TestBlacklistReasonShort(t *testing.T) {
	cases := map[string]string{
		"malware":   "检测到恶意行为",
		"Malware":   "检测到恶意行为",
		" fraud ":   "涉嫌欺诈或误导",
		"copyright": "侵权投诉",
		"security":  "存在高危漏洞",
		"policy":    "违反社区规范",
		"author":    "应作者要求下架",
		"":          "已被停止分发",
		"  ":        "已被停止分发",
		"brand-new": "已被停止分发",
	}
	for code, want := range cases {
		if got := blacklistReasonShort(code); got != want {
			t.Errorf("blacklistReasonShort(%q) = %q, want %q", code, got, want)
		}
	}
}

// TestBlacklistEmbeddedMatchesRepoList 出厂内嵌清单必须与仓库 lists/blacklist.json
// TestBlacklistEmbeddedSnapshotIsEmpty 出厂内嵌快照必须为空
// （2026-11-28 用户令「黑名单不要进内嵌快照」）。
//
// 为什么：黑名单是运营内容（改文件即生效、随时续期/撤下）。此前 build.sh
// Step 0.6 会把 lists/blacklist.json 抄进 internal/api/default_blacklist.json
// 并 go:embed，导致 ① 每次续期都要重建发版；② 同一版本号可能对应不同下架集合；
// ③ 测试/演示条目一旦被内嵌，会在 until 之前对所有用户生效。
//
// 现行语义：清单只走远端（多镜像 + 验签 + TTL + last-known-good 落盘）；
// 从未拉取成功 → fail-open（空清单，绝不误杀），见 blacklist.go 头部注释。
//
// 反向验证：把任一 entry 塞回 internal/api/default_blacklist.json，本用例必须 FAIL。
func TestBlacklistEmbeddedSnapshotIsEmpty(t *testing.T) {
	got := embeddedBlacklist()
	if len(got) != 0 {
		t.Fatalf("出厂内嵌快照必须为空（黑名单只走远端），实际 %d 条：%+v", len(got), got)
	}
}

// TestBlacklistNeverFetchedFailOpen 从未成功拉取 → fail-open（空清单）。
// 反向验证：把 embeddedBlacklist() 换成返回任意一条固定条目，本用例必须 FAIL。
func TestBlacklistNeverFetchedFailOpen(t *testing.T) {
	s, _ := newBlacklistEnv(t, "", http.StatusInternalServerError)
	if _, _, err := s.refreshBlacklist(context.Background()); err == nil {
		t.Fatal("清单源 500 时 refresh 必须报错")
	}
	if entries := s.blacklistEntries(); len(entries) != 0 {
		t.Fatalf("从未拉取成功时必须 fail-open（0 条），实际 %d 条", len(entries))
	}
	if s.isBlacklisted("global-radio", "", "6ce351d7dfa964d7e76d7c53faa217502794ed9859955e97e028935dd2dd7666") {
		t.Fatal("无清单时不得凭历史演示条目命中（不得把运营条目固化进二进制）")
	}
	if s.isBlacklisted("definitely-not-in-blacklist-app", "A", "") {
		t.Fatal("未命中的应用不得被判为下架（不得误杀）")
	}
	got := s.annotateBlacklisted([]AppInfo{{Key: "definitely-not-in-blacklist-app", AppName: "definitely-not-in-blacklist-app", Source: "A"}})
	if len(got) != 1 || got[0].Blacklisted {
		t.Fatalf("未命中的应用不得打标，实际 %+v", got)
	}
}

// TestBlacklistLastKnownGood 拉取失败保留上一次成功清单。
func TestBlacklistLastKnownGood(t *testing.T) {
	s, env := newBlacklistEnv(t, blPayload, http.StatusOK)
	e1, changed, err := s.refreshBlacklist(context.Background())
	if err != nil {
		t.Fatalf("首次拉取应成功: %v", err)
	}
	if !changed || len(e1) != 4 {
		t.Fatalf("首次拉取应标记 changed 且有 4 条，实际 changed=%v n=%d", changed, len(e1))
	}
	if !s.isBlacklisted("badapp", "任意源", "") {
		t.Fatal("首次拉取后 badapp 应命中")
	}

	// 源挂掉 + TTL 过期 → 刷新失败，但清单仍是上一次成功值。
	env.set("", http.StatusInternalServerError)
	st := s.blacklistState()
	st.mu.Lock()
	st.at = time.Now().Add(-blacklistTTL - time.Minute)
	st.mu.Unlock()
	e2, changed2, err2 := s.refreshBlacklist(context.Background())
	if err2 == nil {
		t.Fatal("源 500 时 refresh 必须报错")
	}
	if changed2 {
		t.Fatal("拉取失败不得把清单标记为变化")
	}
	if len(e2) != 4 {
		t.Fatalf("拉取失败应保留 last-known-good（4 条），实际 %d", len(e2))
	}
	if !s.isBlacklisted("badapp", "任意源", "") {
		t.Fatal("last-known-good 生效期内 badapp 仍应命中")
	}
}

// TestBlacklistBlockMessages 动作拦截文案：malware 用恶意版，其余（含未知/
// 空原因码）一律社区规范版——文案是 2026-11-22 用户定稿，不得改写。
func TestBlacklistBlockMessages(t *testing.T) {
	cases := []struct {
		reason string
		want   string
	}{
		{"malware", blacklistMsgMalware},
		{"policy", blacklistMsgCommunity},
		{"copyright", blacklistMsgCommunity},
		{"", blacklistMsgCommunity},
	}
	for _, c := range cases {
		s := &Server{Cfg: &config.Config{}}
		injectBlacklist(s, []blacklistEntry{{App: "victim", Source: "*", Reason: c.reason}})
		if got := s.blacklistBlockMessage("victim", "任意源", ""); got != c.want {
			t.Errorf("reason=%q 文案应为 %q，实际 %q", c.reason, c.want, got)
		}
	}
	if blacklistMsgMalware != "该应用存在恶意行为，已被停止分发，无法下载" {
		t.Errorf("恶意类文案被改动: %q", blacklistMsgMalware)
	}
	if blacklistMsgCommunity != "该应用违反社区规范，已被停止分发，无法下载" {
		t.Errorf("社区规范类文案被改动: %q", blacklistMsgCommunity)
	}
}

// TestBlacklistDetailMessageByKey 详情 404 文案带原因；`appname@源名` 与裸
// appname 两种 key 都要能还原。
func TestBlacklistDetailMessageByKey(t *testing.T) {
	s := &Server{Cfg: &config.Config{}}
	injectBlacklist(s, []blacklistEntry{
		{App: "badapp", Source: "*", Reason: "copyright"},
		{App: "weirdapp", Source: "*", Reason: "unknown-code"},
	})
	msg := s.blacklistDetailMessageByKey("badapp@SomeSource")
	if msg != "该应用已被停止分发（原因：侵权投诉）" {
		t.Errorf("详情文案不对: %q", msg)
	}
	if msg2 := s.blacklistDetailMessageByKey("badapp"); msg2 != msg {
		t.Errorf("裸 appname key 文案应与带源一致: %q", msg2)
	}
	if msg3 := s.blacklistDetailMessageByKey("weirdapp"); msg3 != "该应用已被停止分发（原因：unknown-code）" {
		t.Errorf("未知原因码应原样回显: %q", msg3)
	}
	// 未命中：文案仍可读（调用方只在 hit 时使用），且不得 panic。
	if msg4 := s.blacklistDetailMessageByKey("cleanapp"); msg4 == "" {
		t.Error("未命中时也不得返回空串")
	}
}

// TestBlacklistRefreshChangedOnlyWhenContentDiffers 内容不变 → 不重复失效目录。
func TestBlacklistRefreshChangedOnlyWhenContentDiffers(t *testing.T) {
	s, _ := newBlacklistEnv(t, blPayload, http.StatusOK)
	if _, changed, err := s.refreshBlacklist(context.Background()); err != nil || !changed {
		t.Fatalf("首次加载应 changed=true err=nil，实际 changed=%v err=%v", changed, err)
	}
	if _, changed, err := s.refreshBlacklist(context.Background()); err != nil || changed {
		t.Fatalf("内容未变应 changed=false err=nil，实际 changed=%v err=%v", changed, err)
	}
}

// TestBlacklistRejectsWildcardApp 通配应用名必须被拒绝：`app:"*"` 一行写错
// 就会把整个商店隐藏掉（源名 `*` 仍然合法）。
func TestBlacklistRejectsWildcardApp(t *testing.T) {
	entries := normalizeBlacklist([]blacklistEntry{
		{App: "*", Source: "*", Reason: "malware"},
		{App: "  *  ", Source: "", Reason: "policy"}, // 带空白也要拒
		{App: "badapp", Source: "*", Reason: "policy"},
		{App: "srcapp", Source: "*", Reason: "policy"}, // 源名 * = 合法
	})
	if len(entries) != 2 {
		t.Fatalf("通配 app 条目必须被丢弃（只留两条正常条目），实际 %+v", entries)
	}
	if _, ok := blacklistHitIn(entries, "anything", "", "", time.Now()); ok {
		t.Fatal("通配条目被拒后，任意应用都不得命中")
	}
	if _, ok := blacklistHitIn(entries, "srcapp", "别的源", "", time.Now()); !ok {
		t.Fatal("源名 `*`（全源下架）仍然合法，不得被误删")
	}

	// Server 路径再验一次（injectBlacklist 也走 normalizeBlacklist）。
	s := &Server{Cfg: &config.Config{}}
	injectBlacklist(s, []blacklistEntry{{App: "*", Source: "*"}, {App: "badapp", Source: "*"}})
	if s.isBlacklisted("anything", "", "") {
		t.Fatal("isBlacklisted(anything) 必须为 false（通配条目被拒）")
	}
	if !s.isBlacklisted("badapp", "任意源", "") {
		t.Fatal("正常条目不得被通配拒绝连坐")
	}
}

// TestBlacklistLocalInstallBlock 本地包安装闸：按应用名整名拦截（忽略源名），
// malware 用恶意文案；until 过期 / 其它应用一律放行。
func TestBlacklistLocalInstallBlock(t *testing.T) {
	s := &Server{Cfg: &config.Config{}}
	injectBlacklist(s, []blacklistEntry{
		{App: "badapp", Source: "*", Reason: "malware"},
		{App: "srcapp", Source: "某源", Reason: "policy"},
		{App: "goneapp", Source: "*", Reason: "malware", Until: "2020-01-01"},
	})

	// ① 全源恶意条目：本地包照样拦，文案用恶意版（大小写不敏感）。
	if msg, blocked := s.blacklistLocalInstallBlock("BADAPP", ""); !blocked || msg != blacklistMsgMalware {
		t.Fatalf("全源恶意条目应按应用名拦并给恶意文案，实际 blocked=%v msg=%q", blocked, msg)
	}
	// ② 指定源条目：本地包拿不到源名，仍按应用名拦（宁可多拦）。
	if msg, blocked := s.blacklistLocalInstallBlock("srcapp", ""); !blocked || msg != blacklistMsgCommunity {
		t.Fatalf("指定源条目也应按应用名拦（社区规范文案），实际 blocked=%v msg=%q", blocked, msg)
	}
	// ③ until 过期：不拦。
	if msg, blocked := s.blacklistLocalInstallBlock("goneapp", ""); blocked || msg != "" {
		t.Fatalf("until 过期条目不得拦截，实际 blocked=%v msg=%q", blocked, msg)
	}
	// ④ 其它应用：不拦。
	if msg, blocked := s.blacklistLocalInstallBlock("goodapp", ""); blocked || msg != "" {
		t.Fatalf("未命中应用不得拦截，实际 blocked=%v msg=%q", blocked, msg)
	}
}

// TestListManualUpdatesFiltersBlacklisted 手动更新队列必须过滤已下架应用：
// 负缓存里 badapp/goodapp 都在（reason=wizard，1h 后到期），注入 badapp 黑名单
// 后队列只应剩 goodapp，且返回结构不变（count/items）。
func TestListManualUpdatesFiltersBlacklisted(t *testing.T) {
	s := negCacheServer(t) // MOO_DATA = t.TempDir()
	until := time.Now().Add(time.Hour).Format(time.RFC3339)
	body := `{"schema_version":2,"entries":[
 {"app":"badapp","version":"2.0.0","reason":"wizard","until":"` + until + `"},
 {"app":"goodapp","version":"1.0.0","reason":"wizard","until":"` + until + `"}
]}`
	if err := os.WriteFile(filepath.Join(config.DataDir(), autoUpdateSkipFileName), []byte(body), 0o600); err != nil {
		t.Fatalf("写负缓存失败: %v", err)
	}
	injectBlacklist(s, []blacklistEntry{{App: "badapp", Source: "*", Reason: "policy"}})

	rec := httptest.NewRecorder()
	s.listManualUpdates(rec, httptest.NewRequest("GET", "/api/updates/manual", nil))
	var got struct {
		Count int                `json:"count"`
		Items []manualUpdateItem `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应不是合法 JSON: %v (%s)", err, rec.Body.String())
	}
	if got.Count != 1 || len(got.Items) != 1 {
		t.Fatalf("已下架应用必须从手动队列剔除，count=%d items=%+v", got.Count, got.Items)
	}
	if got.Items[0].App != "goodapp" {
		t.Fatalf("留下的应是 goodapp，实际 %+v", got.Items[0])
	}
}

// TestBlacklistSignatureEnforced（批次③）：清单必须带有效 minisign 签名。
// 签名正确 → 正常加载；正文被篡改（签名不匹配）→ 该候选失败且保留旧值；缺签名 → 失败。
func TestBlacklistSignatureEnforced(t *testing.T) {
	s, env := newBlacklistEnv(t, blPayload, http.StatusOK)
	if _, _, err := s.refreshBlacklist(context.Background()); err != nil {
		t.Fatalf("签名正确时应加载成功: %v", err)
	}

	// 正文多一个字符、签名仍是旧的 → 必须拒绝，且不得标记变化。
	env.setUnsigned(blPayload + " ")
	if _, changed, err := s.refreshBlacklist(context.Background()); err == nil {
		t.Fatal("正文与签名不匹配时必须报错")
	} else if changed {
		t.Fatal("验签失败不得标记清单已变化")
	}
	if !s.isBlacklisted("badapp", "任意源", "") {
		t.Fatal("验签失败时应保留 last-known-good")
	}

	// 缺签名 → 同样拒绝。
	env.setSig("")
	if _, _, err := s.refreshBlacklist(context.Background()); err == nil {
		t.Fatal("缺少 .minisig 时必须报错")
	}
}

// TestBlacklistSignatureRejectsForeignKey：别的私钥签的清单（自洽但公钥不匹配）必须被拒。
func TestBlacklistSignatureRejectsForeignKey(t *testing.T) {
	_, otherPriv, gerr := minisign.GenerateKey(rand.Reader)
	if gerr != nil {
		t.Fatalf("生成临时密钥失败: %v", gerr)
	}
	s, env := newBlacklistEnv(t, blPayload, http.StatusOK)
	env.setSig(string(minisign.Sign(otherPriv, []byte(blPayload))))
	if _, _, err := s.refreshBlacklist(context.Background()); err == nil {
		t.Fatal("非内嵌公钥对应的签名必须被拒绝")
	}
}

// TestBlacklistRepoSignatureValid 仓库里的两份清单必须与各自的 .minisig 匹配
// （用**生产内嵌公钥**验签）。改清单忘记重签名 → 这里红，避免线上静默退化成
// fail-open（新客户端拉不到有效清单 = 下架不生效）。
func TestBlacklistRepoSignatureValid(t *testing.T) {
	for _, rel := range []string{"../../lists/blacklist.json", "../../blacklist.json"} {
		msg, err := os.ReadFile(rel)
		if err != nil {
			t.Fatalf("读 %s 失败: %v", rel, err)
		}
		sig, err := os.ReadFile(rel + ".minisig")
		if err != nil {
			t.Fatalf("读 %s.minisig 失败（改清单后必须重新签名）: %v", rel, err)
		}
		if err := verifyBlacklistSignature(msg, sig); err != nil {
			t.Fatalf("%s 验签失败（清单改了但没重签？）: %v", rel, err)
		}
	}
}

// ---------------------------------------------------------------------------
// 2026-11-22 批次⑥：黑名单精准化（sha256 包指纹 + AppName 判定核查）
// 指纹语义 = AND 的一环，且**调用方 sha 未知时不命中**（防误伤新版本）。
// ---------------------------------------------------------------------------

const (
	blShaA  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	blShaB  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	blShaC  = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	blShaAU = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
)

// TestBlacklistShaMatchRules 规格 §7-1/2/3/5：app-only 命中、源限定、sha 相同
// 命中、同 app 不同 sha 不命中、sha 未知不命中、大小写+空白归一。
func TestBlacklistShaMatchRules(t *testing.T) {
	entries := normalizeBlacklist([]blacklistEntry{
		{App: "badapp", Source: "*", Reason: "malware"}, // app-only（不约束指纹）
		{App: "fpapp", Source: "*", Sha256: blShaA, Reason: "malware"},
		{App: "srcapp", Source: "限定源", Reason: "policy"},
	})
	if len(entries) != 3 {
		t.Fatalf("应解析出 3 条，实际 %d（%+v）", len(entries), entries)
	}
	if entries[1].Sha256 != blShaA {
		t.Fatalf("指纹应归一化保留，实际 %q", entries[1].Sha256)
	}
	now := time.Now()
	cases := []struct {
		name   string
		app    string
		source string
		sha    string
		want   bool
	}{
		{"app-only 全源命中（sha 无关）", "badapp", "随便源", blShaC, true},
		{"app-only 指定源命中", "srcapp", "限定源", "", true},
		{"app-only 其它源不命中", "srcapp", "别的源", "", false},
		{"app+sha 相同 → 命中", "fpapp", "随便源", blShaA, true},
		{"app+sha 同 app 不同 sha → 不命中", "fpapp", "随便源", blShaB, false},
		{"app+sha 调用方 sha 未知 → 不命中", "fpapp", "随便源", "", false},
		{"sha 大写+带空白归一后命中", "fpapp", "随便源", " " + blShaAU + "\t", true},
	}
	for _, c := range cases {
		_, got := blacklistHitIn(entries, c.app, c.source, c.sha, now)
		if got != c.want {
			t.Errorf("%s：app=%q source=%q sha=%q want=%v got=%v", c.name, c.app, c.source, c.sha, c.want, got)
		}
	}
}

// TestBlacklistShaOnly 规格 §7-4：sha-only 条目（app 空）按指纹拦任意名字；
// 不同 sha / sha 未知都不命中。
func TestBlacklistShaOnly(t *testing.T) {
	entries := normalizeBlacklist([]blacklistEntry{{Sha256: blShaB, Reason: "malware"}})
	if len(entries) != 1 || entries[0].App != "" {
		t.Fatalf("sha-only 条目必须保留且 app 为空：%+v", entries)
	}
	now := time.Now()
	if _, ok := blacklistHitIn(entries, "任意应用", "任意源", blShaB, now); !ok {
		t.Fatal("sha-only：同指纹必须命中（不看应用名）")
	}
	if _, ok := blacklistHitIn(entries, "任意应用", "任意源", " "+blShaB+"\t", now); !ok {
		t.Fatal("sha-only：带空白/大写归一后必须命中")
	}
	if _, ok := blacklistHitIn(entries, "任意应用", "任意源", blShaA, now); ok {
		t.Fatal("sha-only：不同 sha 不得命中")
	}
	if _, ok := blacklistHitIn(entries, "任意应用", "任意源", "", now); ok {
		t.Fatal("sha-only：调用方 sha 未知不得命中")
	}
}

// TestBlacklistNormalizeDropsBadEntries 规格 §7-6/7：非法指纹条目与
// 「app 空 + sha 空」条目只丢自己，同清单其它条目仍生效。
func TestBlacklistNormalizeDropsBadEntries(t *testing.T) {
	entries := normalizeBlacklist([]blacklistEntry{
		{App: "badsha", Sha256: "zzzz"},             // 非 hex
		{App: "shortsha", Sha256: "abcd1234"},       // 长度不对
		{Sha256: "  "},                              // app 空 + sha 空
		{App: "   ", Source: "x"},                   // 同上（纯空白）
		{App: "*", Sha256: blShaA},                  // 通配 app 仍拒
		{App: "Keep", Sha256: " " + blShaAU + "\n"}, // 大写 + 空白 → 保留并小写
	})
	if len(entries) != 1 {
		t.Fatalf("坏条目必须只丢自己（应剩 1 条），实际 %d：%+v", len(entries), entries)
	}
	if e := entries[0]; e.App != "Keep" || e.Sha256 != blShaA {
		t.Fatalf("保留条目应归一化为 Keep/%s，实际 %+v", blShaA, e)
	}
	if _, ok := blacklistHitIn(entries, "keep", "", blShaA, time.Now()); !ok {
		t.Fatal("坏数据连坐：同清单里的正常条目必须仍然生效")
	}
}

// TestBlacklistDisplayNameNotUsed 规格 §7-8：目录闸只认 AppName——DisplayName
// 与被拦名相同但 AppName 不同的卡**不得**被剔除；app+sha 条目在 sha 不同时
// 同样放行（新版本不误伤）。
func TestBlacklistDisplayNameNotUsed(t *testing.T) {
	s := &Server{Cfg: &config.Config{}}
	injectBlacklist(s, []blacklistEntry{
		{App: "bannedname", Source: "*", Reason: "malware"},
		{App: "fpapp", Source: "*", Sha256: blShaA, Reason: "malware"},
	})
	in := []AppInfo{
		{Key: "innocent@A", AppName: "innocent", DisplayName: "bannedname", Source: "A"},
		{Key: "fpapp@A", AppName: "fpapp", DisplayName: "fpapp", Source: "A", Sha256: blShaB},
		{Key: "fpapp@B", AppName: "fpapp", DisplayName: "fpapp", Source: "B", Sha256: blShaA},
	}
	got := s.annotateBlacklisted(in)
	if len(got) != len(in) {
		t.Fatalf("打标不得删卡，实际 %d/%d: %+v", len(got), len(in), got)
	}
	for _, a := range got {
		switch a.Key {
		case "fpapp@B":
			if !a.Blacklisted {
				t.Fatalf("指纹命中（fpapp@B / %s）应打标: %+v", blShaA, a)
			}
		default:
			if a.Blacklisted {
				t.Fatalf("DisplayName 同名（%s）与旧指纹都不得误伤: %+v", a.Key, a)
			}
		}
	}
}

// TestBlacklistLocalInstallSha 本地包闸带指纹：同指纹拦、不同指纹放行、
// 指纹未知时 app+sha 条目让路（app-only 条目照旧拦）。
func TestBlacklistLocalInstallSha(t *testing.T) {
	s := &Server{Cfg: &config.Config{}}
	injectBlacklist(s, []blacklistEntry{
		{App: "fpapp", Source: "*", Sha256: blShaA, Reason: "malware"},
		{App: "nameonly", Source: "*", Reason: "policy"},
		{Sha256: blShaB, Reason: "malware"},
	})
	if msg, blocked := s.blacklistLocalInstallBlock("fpapp", blShaA); !blocked || msg != blacklistMsgMalware {
		t.Fatalf("指纹相同必须拦并给恶意文案，实际 blocked=%v msg=%q", blocked, msg)
	}
	if msg, blocked := s.blacklistLocalInstallBlock("FPAPP", blShaA); !blocked || msg != blacklistMsgMalware {
		t.Fatalf("应用名大小写不敏感，实际 blocked=%v msg=%q", blocked, msg)
	}
	if msg, blocked := s.blacklistLocalInstallBlock("fpapp", blShaC); blocked || msg != "" {
		t.Fatalf("同 app 不同指纹（新版本）不得拦，实际 blocked=%v msg=%q", blocked, msg)
	}
	if msg, blocked := s.blacklistLocalInstallBlock("fpapp", ""); blocked || msg != "" {
		t.Fatalf("指纹未知时 app+sha 条目不得命中，实际 blocked=%v msg=%q", blocked, msg)
	}
	if msg, blocked := s.blacklistLocalInstallBlock("nameonly", ""); !blocked || msg != blacklistMsgCommunity {
		t.Fatalf("app-only 条目指纹未知也要拦，实际 blocked=%v msg=%q", blocked, msg)
	}
	if msg, blocked := s.blacklistLocalInstallBlock("other", blShaB); !blocked || msg != blacklistMsgMalware {
		t.Fatalf("sha-only 条目按指纹拦任意名字，实际 blocked=%v msg=%q", blocked, msg)
	}
}

// TestSha256File 规格 §7-9：流式哈希正确 + 不存在路径报错。
func TestSha256File(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pkg.fpk")
	if err := os.WriteFile(p, []byte("hello"), 0o600); err != nil {
		t.Fatalf("写测试文件失败: %v", err)
	}
	const want = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	got, err := sha256File(p)
	if err != nil {
		t.Fatalf("sha256File: %v", err)
	}
	if got != want {
		t.Fatalf("sha256 不对：want %s got %s", want, got)
	}
	if _, err := sha256File(filepath.Join(dir, "missing.fpk")); err == nil {
		t.Fatal("不存在的路径必须返回 error")
	}
}

// P1（2026-11-22）：last-known-good 落盘 —— 重启/断网后仍能沿用上次验签通过的清单。
func TestBlacklistLKGRoundTrip(t *testing.T) {
	s, _ := newBlacklistEnv(t, blPayload, http.StatusOK)
	lkg := filepath.Join(t.TempDir(), "blacklist_last.json")
	s.BlacklistLKGPath = lkg
	if _, changed, err := s.refreshBlacklist(context.Background()); err != nil || !changed {
		t.Fatalf("首次刷新应成功且变更: err=%v changed=%v", err, changed)
	}
	fi, err := os.Stat(lkg)
	if err != nil {
		t.Fatalf("快照未落盘: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("快照权限 = %o，期望 600", perm)
	}
	// 新进程（模拟重启）：配置指向不可达地址，仍应立刻用磁盘快照。
	offline := &Server{Cfg: &config.Config{ListBaseURL: "http://127.0.0.1:1/lists", SourceListAllowPrivate: true}, BlacklistLKGPath: lkg}
	entries := offline.blacklistEntries()
	if len(entries) == 0 {
		t.Fatalf("重启后未从快照恢复清单")
	}
	if !offline.isBlacklisted("BadApp", "", "") {
		t.Fatalf("快照恢复后 BadApp 应被拦截")
	}
}

// 损坏/空快照 = 无快照（fail-open），且不得 panic。
func TestBlacklistLKGCorruptIgnored(t *testing.T) {
	lkg := filepath.Join(t.TempDir(), "blacklist_last.json")
	if err := os.WriteFile(lkg, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := len(embeddedBlacklist())
	s := &Server{Cfg: &config.Config{ListBaseURL: "http://127.0.0.1:1/lists", SourceListAllowPrivate: true}, BlacklistLKGPath: lkg}
	if entries := s.blacklistEntries(); len(entries) != want {
		t.Fatalf("损坏快照应回落出厂内嵌快照（%d 条），实际 %d 条", want, len(entries))
	}
	if err := os.WriteFile(lkg, []byte(`{"saved_at":"2026-11-22T00:00:00Z","entries":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s2 := &Server{Cfg: &config.Config{ListBaseURL: "http://127.0.0.1:1/lists", SourceListAllowPrivate: true}, BlacklistLKGPath: lkg}
	if entries := s2.blacklistEntries(); len(entries) != want {
		t.Fatalf("空快照应回落出厂内嵌快照（%d 条），实际 %d 条", want, len(entries))
	}
}

// P2（2026-11-22）：镜像前缀候选也要过安全策略（不能借未签名镜像清单打内网）。
func TestBlacklistCandidateAllowed(t *testing.T) {
	if err := blacklistCandidateAllowed(nil, "http://169.254.1.5:8080/x"); err == nil {
		t.Fatalf("默认配置下非公网候选必须被拒绝")
	}
	if err := blacklistCandidateAllowed(&config.Config{}, "http://224.0.0.1/x"); err == nil {
		t.Fatalf("组播候选必须被拒绝")
	}
	if err := blacklistCandidateAllowed(&config.Config{SourceListAllowPrivate: true}, "http://169.254.1.5:8080/x"); err != nil {
		t.Fatalf("测试放行配置下不应拒绝: %v", err)
	}
	if err := blacklistCandidateAllowed(nil, "https://raw.githubusercontent.com/x/y/main/blacklist.json"); err != nil {
		t.Fatalf("公网候选不应被拒绝: %v", err)
	}
}

// TestBlacklistFailedFetchBacksOff P1-3（2026-11-22）：从未加载成功时，
// 拉取失败后 blacklistFallbackTTL 窗口内目录热路径**不得**再起拉取链；
// 窗口过后必须恢复尝试。反向验证：把失败分支改回无条件 `now-(TTL-fallback)`
// （= since 恒 ≥ fallback），本用例必须 FAIL。
func TestBlacklistFailedFetchBacksOff(t *testing.T) {
	s, env := newBlacklistEnv(t, "", http.StatusInternalServerError)
	// 共享状态表按地址串缓存：显式清一次，避免别处同地址留下的 loaded/at 干扰。
	st := s.blacklistState()
	st.mu.Lock()
	st.entries, st.loaded, st.at, st.triedLKG, st.refreshing = nil, false, time.Time{}, false, false
	st.mu.Unlock()

	if _, _, err := s.refreshBlacklist(context.Background()); err == nil {
		t.Fatal("源 500 时 refresh 必须报错")
	}
	afterFail := env.hitCount()
	if afterFail == 0 {
		t.Fatal("首次拉取失败也应有网络请求（用例前提不成立）")
	}

	// 退避窗口内连续走热路径：不得触发新的后台拉取。
	for i := 0; i < 6; i++ {
		if entries := s.blacklistEntries(); len(entries) != len(embeddedBlacklist()) {
			t.Fatalf("未加载成功时热路径必须回出厂快照")
		}
		time.Sleep(30 * time.Millisecond) // 给误起的 goroutine 留出发请求的时间
	}
	if got := env.hitCount(); got != afterFail {
		t.Fatalf("失败退避窗口内不得再拉取：请求数 %d → %d", afterFail, got)
	}

	// 窗口过期 → 恢复尝试（后台单飞）。
	st.mu.Lock()
	st.at = time.Now().Add(-blacklistFallbackTTL - time.Second)
	st.mu.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_ = s.blacklistEntries()
		if env.hitCount() > afterFail {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("退避窗口过期后应重新尝试拉取")
}

// TestBlacklistNoTestFixtures 守护（P1-1，2026-11-22）：出厂内嵌快照与仓库清单
// 都不得夹带「测试/演示」条目。演示条目一旦随二进制发出，所有用户在该条目的
// until 之前都会装不上对应应用（本仓曾把 global-radio 演示条目写进两处）。
// 判据：ref / notice 含 test（大小写不敏感）或「演示」即视为测试夹具。
// 反向验证：任一处加回 ref:"test-xxx" 条目，本用例必须 FAIL。
func TestBlacklistNoTestFixtures(t *testing.T) {
	check := func(where string, entries []blacklistEntry) {
		t.Helper()
		for _, e := range entries {
			ref, notice := e.Ref, e.Notice
			lowRef, lowNotice := strings.ToLower(ref), strings.ToLower(notice)
			if strings.Contains(lowRef, "test") || strings.Contains(lowNotice, "test") ||
				strings.Contains(ref, "演示") || strings.Contains(notice, "演示") {
				t.Fatalf("%s 夹带测试/演示条目：app=%q ref=%q notice=%q", where, e.App, ref, notice)
			}
		}
	}
	check("出厂内嵌快照（internal/api/default_blacklist.json）", embeddedBlacklist())

	b, err := os.ReadFile(filepath.Join("..", "..", "lists", "blacklist.json"))
	if err != nil {
		t.Fatalf("读取仓库清单失败: %v", err)
	}
	repo, err := parseBlacklist(string(b))
	if err != nil {
		t.Fatalf("仓库清单解析失败: %v", err)
	}
	check("仓库清单（lists/blacklist.json）", repo)
}
