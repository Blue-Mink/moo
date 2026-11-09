package main

import (
	"log"
	"net/http"
	"strings"
	"time"
)

// mateHTTPClient 探测用 HTTP 客户端（8s 超时，与 update.go 的探测习惯一致）。
func mateHTTPClient() *http.Client { return mateExternalClient(8 * time.Second) }

// mateTokenFn 由 companion 注入的取 token 钩子（单测可覆写为固定值/nil）。
var mateTokenFn func() string

// probeMateUpdateCached 复用 update_api.go 的 6h/10min 缓存探测。
func probeMateUpdateCached() (mateUpdateInfo, error) {
	tok := ""
	if mateTokenFn != nil {
		tok = mateTokenFn()
	}
	info := mateUpdateCached(true, tok)
	if info.LastError != "" {
		return info, errString(info.LastError)
	}
	return info, nil
}

// errString 极简 error 包装（字符串即错误）。
type errString string

func (e errString) Error() string { return string(e) }

// logf 统一日志出口。
func logf(format string, args ...any) {
	log.Printf(format, args...)
}

// mateReleaseAssets 取最新 mate-v* release 的完整资产列表（含 .sha256 / .minisig
// 侧车）与其版本号。与 probeMateUpdate 同样的通道顺序，但需要资产列表，所以
// atom 通道跳过。
//
// 返回值带版本：不同通道（api / 镜像 / atom）可能短暂不同步，探测到的
// `available_version` 与这里挑出的「最高 release」未必是同一版。调用方必须
// 核对版本一致才用这份资产，否则可能装成另一版（见 mateSelfUpdateAssetURLs）。
func mateReleaseAssets() ([]mateReleaseAsset, string, error) {
	tok := ""
	if mateTokenFn != nil {
		tok = mateTokenFn()
	}
	client := mateHTTPClient()
	var errs []string
	for _, p := range mateUpdateProviders {
		if p.name == "atom" {
			continue // atom 没有资产列表
		}
		t := ""
		if p.auth {
			t = tok
		}
		rels, err := fetchMateReleases(client, p.url, t)
		if err != nil {
			errs = append(errs, p.name+": "+err.Error())
			continue
		}
		// 与探测通道共用同一份筛选逻辑（跳过 draft / prerelease / 非 mate-v 版本 tag），
		// 避免「探测到 X、资产却按 Y 取」这类规则漂移。
		best, bestVer := latestMateRelease(rels)
		if best == nil {
			errs = append(errs, p.name+": release 列表里没有 mate-v*")
			continue
		}
		return best.Assets, bestVer, nil
	}
	if len(errs) == 0 {
		errs = []string{"无可用通道"}
	}
	return nil, "", errString(strings.Join(errs, " / "))
}

// handleSelfUpdate POST /api/self-update：触发后台自更新。
func (c *companion) handleSelfUpdate(w http.ResponseWriter, r *http.Request) {
	target, err := startMateSelfUpdate()
	if err != nil {
		mateSelfUpdate.mu.Lock()
		running := mateSelfUpdate.cur.Running
		mateSelfUpdate.mu.Unlock()
		code := http.StatusBadRequest
		if running {
			code = http.StatusConflict
		}
		writeJSON(w, code, map[string]any{"started": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"started": true, "target_version": target})
}

// handleSelfUpdateStatus GET /api/self-update/status：状态机快照。
func (c *companion) handleSelfUpdateStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, mateSelfUpdateStatus())
}
