package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"moo/internal/config"
)

// 0.6.512：加速源清单目录化。lists/github_mirrors.txt 与 lists/docker_mirrors.txt
// 一行一条 `key|label|url|description`（url 可空，对应 auto/custom/direct）。
// 远端清单拉取成功即覆盖内置清单；失败保留上一次成功值 / 内置清单，离线不受影响。
// 「删除默认源」= 本机屏蔽集合（config.HiddenMirrors），「恢复」= 清空并重拉。

// parseMirrorList 解析加速源清单文本（容错：缺列跳过、同 key 只留第一条）。
func parseMirrorList(body string) []config.MirrorOption {
	out := make([]config.MirrorOption, 0, 24)
	seen := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 2 {
			continue
		}
		key, label := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if key == "" || label == "" || seen[key] {
			continue
		}
		opt := config.MirrorOption{Key: key, Label: label}
		if len(parts) > 2 {
			opt.URL = strings.TrimSpace(parts[2])
		}
		if len(parts) > 3 {
			opt.Description = strings.TrimSpace(strings.Join(parts[3:], "|"))
		}
		seen[key] = true
		out = append(out, opt)
	}
	return out
}

// fetchMirrorFile 拉一份镜像清单（仅新路径；root 无历史文件）。
func (s *Server) fetchMirrorFile(ctx context.Context, name string) ([]config.MirrorOption, error) {
	var lastErr error
	for _, u := range config.ListFileURLs(s.Cfg.ListBaseURL, name, "") {
		body, err := s.fetchListURL(ctx, u)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		opts := parseMirrorList(body)
		if len(opts) < 2 {
			lastErr = fmt.Errorf("%s 内容为空或格式不符", name)
			continue
		}
		return opts, nil
	}
	if lastErr == nil {
		lastErr = errors.New("无候选地址")
	}
	return nil, lastErr
}

// refreshMirrorLists 拉取两份清单并生效（任一成功即生效，另一份回落内置）。
func (s *Server) refreshMirrorLists(ctx context.Context) (int, int, error) {
	gh, ghErr := s.fetchMirrorFile(ctx, "github_mirrors.txt")
	dk, dkErr := s.fetchMirrorFile(ctx, "docker_mirrors.txt")
	if len(gh) == 0 && len(dk) == 0 {
		return 0, 0, fmt.Errorf("加速源清单拉取失败：github=%v docker=%v", ghErr, dkErr)
	}
	config.SetRemoteMirrorOptions(gh, dk)
	return len(gh), len(dk), nil
}

// WarmMirrorLists 启动时后台预热一次（失败静默，保持内置清单）。
func (s *Server) WarmMirrorLists() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		_, _, _ = s.refreshMirrorLists(ctx)
	}()
}

// mirrorsSync POST /api/mirrors/sync：立即从远端清单刷新加速源（免重建 FPK）。
func (s *Server) mirrorsSync(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	gh, dk, err := s.refreshMirrorLists(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	s.Mirrors.ensureFresh(true)
	writeJSON(w, map[string]any{"ok": true, "github": gh, "docker": dk})
}

// mirrorsRestore POST /api/mirrors/restore：清空本机屏蔽（删掉的默认源全部回来）
// 并重新拉取远端清单。
func (s *Server) mirrorsRestore(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	s.Cfg.HiddenMirrors = nil
	config.SetHiddenMirrors(nil)
	_ = s.Cfg.Save(dataDirOf(s))
	gh, dk, err := s.refreshMirrorLists(ctx)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "restored": true, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "github": gh, "docker": dk})
}

// mirrorsHidden POST /api/mirrors/hidden：本机屏蔽加速源。
// body 支持三种写法：{"keys":[...]} 全量覆盖；{"add":["k"]} 追加；{"remove":["k"]} 移除。
// auto/custom/direct 永不可屏蔽。空 keys 数组 = 全部恢复显示。
func (s *Server) mirrorsHidden(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Keys   []string `json:"keys"`
		Add    []string `json:"add"`
		Remove []string `json:"remove"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	cur := append([]string(nil), s.Cfg.HiddenMirrors...)
	if req.Keys != nil {
		cur = req.Keys
	}
	if len(req.Remove) > 0 {
		drop := map[string]bool{}
		for _, k := range req.Remove {
			drop[k] = true
		}
		kept := cur[:0]
		for _, k := range cur {
			if !drop[k] {
				kept = append(kept, k)
			}
		}
		cur = kept
	}
	have := map[string]bool{}
	for _, k := range cur {
		have[k] = true
	}
	for _, k := range req.Add {
		if !have[k] {
			cur = append(cur, k)
			have[k] = true
		}
	}
	s.Cfg.HiddenMirrors = cur
	config.SetHiddenMirrors(cur)
	if err := s.Cfg.Save(dataDirOf(s)); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "hidden": config.HiddenMirrors()})
}
