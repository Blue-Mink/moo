package api

import (
	"errors"
	"net/http"
	"strings"
	"unicode"
)

// 应用收藏：目录 key（appname 或 appname@源名）的持久化集合。
// 星标按钮（列表行/桌面卡片/详情头部）统一走 toggle 端点，
// 发现页「收藏列表」按 config.Favorites 顺序渲染。

const maxFavorites = 500

// favoritesList GET /api/favorites → {"favorites": [...]}（空 = 空数组）。
func (s *Server) favoritesList(w http.ResponseWriter, _ *http.Request) {
	favs := s.Cfg.Favorites
	if favs == nil {
		favs = []string{}
	}
	writeJSON(w, map[string]any{"favorites": favs})
}

// favoriteToggle POST /api/favorites {"key"}：在收藏集合中切换该 key，
// 返回切换后的完整集合（前端以返回值为准，天然幂等、无竞态丢态）。
func (s *Server) favoriteToggle(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Key string `json:"key"`
	}
	if err := jsonDecode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	key := strings.TrimSpace(in.Key)
	if key == "" {
		writeErr(w, http.StatusBadRequest, errors.New("key 不能为空"))
		return
	}
	if len(key) > 128 {
		writeErr(w, http.StatusBadRequest, errors.New("key 过长"))
		return
	}
	for _, c := range key {
		if unicode.IsControl(c) {
			writeErr(w, http.StatusBadRequest, errors.New("key 含非法字符"))
			return
		}
	}

	found := false
	favs := s.Cfg.Favorites[:0:0] // 重建切片，避免 alias 原底层数组
	for _, k := range s.Cfg.Favorites {
		if k == key {
			found = true
			continue
		}
		favs = append(favs, k)
	}
	var favorited bool
	if found {
		// 取消收藏
		favorited = false
	} else {
		if len(favs) >= maxFavorites {
			writeErr(w, http.StatusConflict, errors.New("收藏数量已达上限（500）"))
			return
		}
		favs = append(favs, key)
		favorited = true
	}
	if favs == nil {
		favs = []string{}
	}
	s.Cfg.Favorites = favs
	_ = s.Cfg.Save(dataDirOf(s))
	writeJSON(w, map[string]any{"favorited": favorited, "favorites": favs})
}
