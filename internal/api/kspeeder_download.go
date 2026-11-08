package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"moo/internal/config"
)

// 0.6.524（用户令）：KSpeeder 安装包不再依赖应用源目录，改读远端清单
// lists/kspeeder.txt —— 一行一个架构：
//
//	arch|version|filename|url|sha256|size
//
// 清单拉取失败沿用上一次成功值（10 分钟 TTL）；从未成功则明确报错。
// 下载后按 sha256 + 字节数双重校验，校验不过不安装。

type ksEntry struct {
	Arch     string `json:"arch"`
	Version  string `json:"version"`
	Filename string `json:"filename"`
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
}

const ksListTTL = 10 * time.Minute

var ksListCache struct {
	mu        sync.Mutex
	at        time.Time
	failUntil time.Time
	entries   []ksEntry
}

// ksDefaultEntries 内置兜底（远端清单未部署 / 断网时仍可安装）。
// 远端 lists/kspeeder.txt 一旦可达即以其为准，改清单不用重建 FPK。
var ksDefaultEntries = []ksEntry{
	{Arch: "x86", Version: "0.8.9", Filename: "kspeeder-0.8.9-fnos-amd64.fpk",
		URL:    "https://github.com/Blue-Mink/FnDepot/releases/download/v0.8.9/kspeeder-0.8.9-fnos-amd64.fpk",
		SHA256: "665e76bca64f8b547b60660f2a5de59dc74adf8211a68bc5f2b6e74836085e06", Size: 6459687},
	{Arch: "arm", Version: "0.8.9", Filename: "kspeeder-0.8.9-fnos-arm64.fpk",
		URL:    "https://github.com/Blue-Mink/FnDepot/releases/download/v0.8.9/kspeeder-0.8.9-fnos-arm64.fpk",
		SHA256: "3224f796115eb1c691b68d297a4b16206773fa274c5aaaf2b0207bf66fefce34", Size: 6111640},
}

// parseKSList 解析 KSpeeder 下载清单（容错：缺列 / 非法行跳过）。
func parseKSList(body string) []ksEntry {
	out := make([]ksEntry, 0, 4)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p := strings.Split(line, "|")
		if len(p) < 6 {
			continue
		}
		size, _ := strconv.ParseInt(strings.TrimSpace(p[5]), 10, 64)
		e := ksEntry{
			Arch:     strings.ToLower(strings.TrimSpace(p[0])),
			Version:  strings.TrimSpace(p[1]),
			Filename: filepath.Base(strings.TrimSpace(p[2])),
			URL:      strings.TrimSpace(p[3]),
			SHA256:   strings.ToLower(strings.TrimSpace(p[4])),
			Size:     size,
		}
		if e.Arch == "" || !strings.HasPrefix(e.URL, "http") || len(e.SHA256) != 64 || e.Size <= 0 {
			continue
		}
		out = append(out, e)
	}
	return out
}

func ksArch() string {
	if runtime.GOARCH == "arm64" {
		return "arm"
	}
	return "x86"
}

func pickKS(entries []ksEntry) (ksEntry, error) {
	want := ksArch()
	for _, e := range entries {
		if e.Arch == want {
			return e, nil
		}
	}
	for _, e := range entries {
		if e.Arch == "x86" {
			return e, nil
		}
	}
	return ksEntry{}, errors.New("清单中没有可用架构条目")
}

// ksDownloadEntry 取当前架构的下载条目（10 分钟缓存；force=true 强制重拉）。
func (s *Server) ksDownloadEntry(ctx context.Context, force bool) (ksEntry, error) {
	ksListCache.mu.Lock()
	cached := append([]ksEntry(nil), ksListCache.entries...)
	fresh := time.Since(ksListCache.at) < ksListTTL
	backoff := time.Now().Before(ksListCache.failUntil)
	ksListCache.mu.Unlock()
	if fresh && !force && len(cached) > 0 {
		return pickKS(cached)
	}
	// 拉取失败退避 2 分钟：UI 打开加速页不因清单 404 反复等待镜像链超时。
	if backoff && !force && len(cached) == 0 {
		return pickKS(ksDefaultEntries)
	}
	var lastErr error
	for _, u := range config.ListFileURLs(s.Cfg.ListBaseURL, "kspeeder.txt", "") {
		body, err := s.fetchListURL(ctx, u)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return ksEntry{}, ctx.Err()
			}
			continue
		}
		entries := parseKSList(body)
		if len(entries) == 0 {
			lastErr = errors.New("kspeeder.txt 内容为空或格式不符")
			continue
		}
		ksListCache.mu.Lock()
		ksListCache.entries = entries
		ksListCache.at = time.Now()
		ksListCache.mu.Unlock()
		return pickKS(entries)
	}
	if len(cached) > 0 {
		return pickKS(cached)
	}
	// 远端清单不可达（尚未发布 / 断网）：用内置兜底，下载仍做 sha256 校验。
	ksListCache.mu.Lock()
	ksListCache.failUntil = time.Now().Add(2 * time.Minute)
	ksListCache.mu.Unlock()
	_ = lastErr
	return pickKS(ksDefaultEntries)
}

// ksMirrorCandidates 下载候选：直连优先，GitHub 资产再叠加健康加速源前缀。
func ksMirrorCandidates(raw string) []string {
	out := []string{raw}
	if !strings.Contains(raw, "github.com/") {
		return out
	}
	for _, opt := range config.GitHubMirrorOptions() {
		if opt.URL != "" && strings.HasSuffix(opt.URL, "/") && opt.Key != "auto" && opt.Key != "custom" && !strings.HasPrefix(opt.Key, "custom-") {
			out = append(out, opt.URL+raw)
		}
	}
	return out
}

// ksFileMatches 判断本地文件是否已是清单里那一份（大小 + sha256）。
func ksFileMatches(p string, e ksEntry) bool {
	st, err := os.Stat(p)
	if err != nil || st.Size() != e.Size {
		return false
	}
	sum, err := sha256File(p)
	return err == nil && sum == e.SHA256
}

func sha256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// downloadKSOne 单候选下载到 tmp（进度回调 0-1，大小未知时按 0 处理）。
func (s *Server) downloadKSOne(ctx context.Context, u, tmp string, size int64, progress func(float64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, 128<<10)
	var done int64
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			done += int64(n)
			if size > 0 && progress != nil {
				progress(float64(done) / float64(size))
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}

// downloadKS 下载并校验（sha256 + 字节数），全部候选失败才返回错误。
func (s *Server) downloadKS(ctx context.Context, e ksEntry, dst string, progress func(float64)) error {
	tmp := dst + ".part"
	var lastErr error
	for _, u := range ksMirrorCandidates(e.URL) {
		if err := s.downloadKSOne(ctx, u, tmp, e.Size, progress); err != nil {
			lastErr = err
			continue
		}
		st, err := os.Stat(tmp)
		if err != nil || st.Size() != e.Size {
			lastErr = fmt.Errorf("下载字节数不符（期望 %d）", e.Size)
			_ = os.Remove(tmp)
			continue
		}
		sum, err := sha256File(tmp)
		if err != nil || sum != e.SHA256 {
			lastErr = errors.New("sha256 校验失败")
			_ = os.Remove(tmp)
			continue
		}
		if err := os.Rename(tmp, dst); err != nil {
			return err
		}
		return nil
	}
	_ = os.Remove(tmp)
	if lastErr == nil {
		lastErr = errors.New("无候选下载地址")
	}
	return lastErr
}

// ksDownloadInfo GET /api/kspeeder/latest（admin）：当前架构的最新下载条目。
func (s *Server) ksDownloadInfo(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	e, err := s.ksDownloadEntry(ctx, false)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]any{
		"ok": true, "arch": e.Arch, "version": e.Version, "filename": e.Filename,
		"url": e.URL, "sha256": e.SHA256, "size": e.Size,
	})
}

// kspeederInstallSSE POST /api/kspeeder/install（admin）：按远端清单下载 +
// 校验 + 安装（与应用源安装走同一暂存/向导/平台安装管线）。
func (s *Server) kspeederInstallSSE(w http.ResponseWriter, r *http.Request) {
	f := sseStart(w)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	sseSend(w, f, map[string]any{"step": "stage", "progress": 2, "message": "读取 KSpeeder 下载清单…"})
	e, err := s.ksDownloadEntry(ctx, false)
	if err != nil {
		sseSend(w, f, map[string]any{"step": "error", "error": err.Error()})
		return
	}
	dir := s.Pipe.Downloads
	if dir == "" {
		dir = defaultDownloadDir()
	}
	dst := filepath.Join(dir, e.Filename)
	if ksFileMatches(dst, e) {
		sseSend(w, f, map[string]any{"step": "download", "progress": 28, "message": "已存在校验通过的安装包"})
	} else {
		sseSend(w, f, map[string]any{"step": "download", "progress": 5, "message": "下载 KSpeeder " + e.Version + "…"})
		err := s.downloadKS(ctx, e, dst, func(frac float64) {
			sseSend(w, f, map[string]any{"step": "download", "progress": 5 + frac*22, "message": "下载 KSpeeder " + e.Version + "…"})
		})
		if err != nil {
			sseSend(w, f, map[string]any{"step": "error", "error": "下载 KSpeeder 失败：" + err.Error()})
			return
		}
	}
	s.installFpkFileSSE(w, f, r, dst)
}
