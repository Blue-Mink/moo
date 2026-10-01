package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"moo/internal/config"
)

type favResp struct {
	Favorited bool     `json:"favorited"`
	Favorites []string `json:"favorites"`
}

func newFavoritesServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("MOO_DATA", dir)
	return &Server{Cfg: config.Default()}
}

func TestFavoritesToggle(t *testing.T) {
	s := newFavoritesServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/favorites", s.favoritesList)
	mux.HandleFunc("POST /api/favorites", s.favoriteToggle)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// 初始空
	res, err := http.Get(ts.URL + "/api/favorites")
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Favorites []string `json:"favorites"`
	}
	json.NewDecoder(res.Body).Decode(&out)
	res.Body.Close()
	if out.Favorites == nil || len(out.Favorites) != 0 {
		t.Fatalf("初始应为空数组: %#v", out.Favorites)
	}

	// 收藏 gitea
	body, _ := json.Marshal(map[string]string{"key": "gitea"})
	res, err = http.Post(ts.URL+"/api/favorites", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var out2 favResp
	json.NewDecoder(res.Body).Decode(&out2)
	res.Body.Close()
	if !out2.Favorited || len(out2.Favorites) != 1 || out2.Favorites[0] != "gitea" {
		t.Fatalf("首次收藏应为 favorited=true: %#v", out2)
	}

	// 再切一次 = 取消
	res, _ = http.Post(ts.URL+"/api/favorites", "application/json", bytes.NewReader(body))
	out2 = favResp{}
	json.NewDecoder(res.Body).Decode(&out2)
	res.Body.Close()
	if out2.Favorited || len(out2.Favorites) != 0 {
		t.Fatalf("二次切换应取消: %#v", out2)
	}

	// 两个 key 并存 + 部分取消
	post := func(key string) favResp {
		b, _ := json.Marshal(map[string]string{"key": key})
		r, _ := http.Post(ts.URL+"/api/favorites", "application/json", bytes.NewReader(b))
		var o favResp
		json.NewDecoder(r.Body).Decode(&o)
		r.Body.Close()
		return o
	}
	post("gitea")
	post("mihomo@Blue-Mink")
	if len(s.Cfg.Favorites) != 2 {
		t.Fatalf("应有 2 条: %#v", s.Cfg.Favorites)
	}
	if o := post("gitea"); o.Favorited {
		t.Fatalf("取消 gitea 后应为 false: %#v", o)
	}
	if len(s.Cfg.Favorites) != 1 || s.Cfg.Favorites[0] != "mihomo@Blue-Mink" {
		t.Fatalf("剩余应为 mihomo@Blue-Mink: %#v", s.Cfg.Favorites)
	}

	// 空 key 拒绝
	res, _ = http.Post(ts.URL+"/api/favorites", "application/json", bytes.NewReader([]byte(`{"key":"  "}`)))
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("空 key 应 400: %d", res.StatusCode)
	}

	// 持久化：同目录重新 Load 应读回
	cfg2, err := config.Load(config.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg2.Favorites) != 1 || cfg2.Favorites[0] != "mihomo@Blue-Mink" {
		t.Fatalf("持久化读回不符: %#v", cfg2.Favorites)
	}
}
