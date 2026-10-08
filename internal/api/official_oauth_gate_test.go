package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"moo/internal/official"
)

// 0.6.312 回归：hasOAuthSession 门控不得把「access 过期但 refresh_token 有效」
// 的会话判为未连接（旧实现如此 → 官方源授权后约 1 小时必转「未连接」）。
func TestHasOAuthSessionGateAllowsExpiredButRefreshable(t *testing.T) {
	mkStore := func(t *testing.T, sess *official.Session) *officialStore {
		t.Helper()
		dir := t.TempDir()
		mgr := official.NewManagerWithBase(dir, "http://127.0.0.1:1")
		if sess != nil {
			b, err := os.CreateTemp(dir, "sess")
			if err != nil {
				t.Fatal(err)
			}
			enc, err := officialSessionJSON(sess)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := b.Write(enc); err != nil {
				t.Fatal(err)
			}
			b.Close()
			os.Rename(b.Name(), filepath.Join(dir, "official_session.json"))
		}
		mgr.LoadSession()
		return &officialStore{mgr: mgr}
	}
	now := time.Now()
	cases := []struct {
		name string
		sess *official.Session
		want bool
	}{
		{"无会话", nil, false},
		{"access 有效", &official.Session{
			AccessToken: "a", RefreshToken: "r",
			ExpiresAt: now.Add(time.Hour).UnixMilli(), UpdatedAt: now.UnixMilli(),
		}, true},
		{"access 过期+有 refresh（应放行给 Do 自动续期）", &official.Session{
			AccessToken: "a", RefreshToken: "r",
			ExpiresAt: now.Add(-time.Hour).UnixMilli(), UpdatedAt: now.Add(-2 * time.Hour).UnixMilli(),
		}, true},
		{"access 过期+无 refresh（需重新授权）", &official.Session{
			AccessToken: "a",
			ExpiresAt: now.Add(-time.Hour).UnixMilli(), UpdatedAt: now.Add(-2 * time.Hour).UnixMilli(),
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mkStore(t, c.sess).hasOAuthSession()
			if got != c.want {
				t.Fatalf("hasOAuthSession=%v, want %v", got, c.want)
			}
		})
	}
}

// officialSessionJSON 复刻 Session 的 JSON 落盘字段（避免测试依赖非导出写盘路径）。
func officialSessionJSON(s *official.Session) ([]byte, error) {
	type wire struct {
		AccessToken  string   `json:"access_token"`
		RefreshToken string   `json:"refresh_token,omitempty"`
		ExpiresAt    int64    `json:"expires_at"`
		Scopes       []string `json:"scopes,omitempty"`
		UpdatedAt    int64    `json:"updated_at"`
	}
	w := wire{
		AccessToken:  s.AccessToken,
		RefreshToken: s.RefreshToken,
		ExpiresAt:    s.ExpiresAt,
		Scopes:       s.Scopes,
		UpdatedAt:    s.UpdatedAt,
	}
	return json.Marshal(w)
}
