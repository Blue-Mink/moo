package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 0.2.73 S5：存量回填。
// 老条目（0.2.72 之前扫描入库）没有 readme_url / releases / changelog。
// 启动 60 秒后跑一轮，此后每轮同步再跑一轮，每轮最多 backfillPerRound 个应用，
// 同一应用 7 天内不重复尝试（避免反复打上游）；只补三个协议字段，
// 绝不改动 version / download_url / packages —— 回填不是重新扫描。

const (
	backfillTTL      = 7 * 24 * time.Hour
	backfillPerRound = 5
)

type backfillState struct {
	At map[string]int64 `json:"at"` // appname -> unix 秒（上次尝试）
}

func (c *companion) backfillStatePath() string {
	return filepath.Join(c.bootDir, "backfill_state.json")
}

func (c *companion) loadBackfillState() backfillState {
	st := backfillState{At: map[string]int64{}}
	b, err := os.ReadFile(c.backfillStatePath())
	if err != nil {
		return st
	}
	_ = json.Unmarshal(b, &st)
	if st.At == nil {
		st.At = map[string]int64{}
	}
	return st
}

func (c *companion) saveBackfillState(st backfillState) {
	now := time.Now().Unix()
	for k, v := range st.At {
		if now-v > int64(30*24*time.Hour/time.Second) {
			delete(st.At, k)
		}
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	p := c.backfillStatePath()
	tmp := p + ".tmp"
	if os.MkdirAll(filepath.Dir(p), 0o755) != nil {
		return
	}
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, p)
	}
}

// needsBackfill 判断条目是否缺协议字段。
func needsBackfill(a *indexedApp) bool {
	if a == nil || a.Source == "" {
		return false
	}
	return a.ReadmeURL == "" || len(a.Releases) == 0 || a.Changelog == ""
}

// pickCandidate 在仓库发现结果里挑出该应用的最佳候选（按文件名匹配 appname）。
func pickCandidate(apps []discoveredApp, appname string) *discoveredApp {
	want := strings.ToLower(appname)
	var best *discoveredApp
	for i := range apps {
		d := &apps[i]
		name := strings.ToLower(appKeyFromFilename(d.Name))
		if name != want && !strings.Contains(strings.ToLower(d.Name), want) {
			continue
		}
		if best == nil || versionGreater(d.Version, best.Version) {
			best = d
		}
	}
	return best
}

// backfillOnce 回填至多 limit 个应用，返回实际补到字段的条数。
// 只读上游元数据，不下载 FPK；失败只记录尝试时间，不影响同步。
func (c *companion) backfillOnce(ctx context.Context, limit int) int {
	if limit <= 0 {
		return 0
	}
	st := c.loadBackfillState()
	now := time.Now()
	bySource := map[string][]*indexedApp{}
	for _, a := range c.idx.list() {
		if !needsBackfill(a) {
			continue
		}
		if _, _, ok := parseGHRepo(a.Source); !ok {
			continue
		}
		if ts, ok := st.At[a.Appname]; ok && now.Sub(time.Unix(ts, 0)) < backfillTTL {
			continue
		}
		bySource[a.Source] = append(bySource[a.Source], a)
	}
	filled, tried := 0, 0
	for src, apps := range bySource {
		if tried >= limit {
			break
		}
		list, err := discoverFromRepo(ctx, src, c.githubToken(), c.httpc)
		if err != nil {
			for _, a := range apps {
				st.At[a.Appname] = now.Unix()
				tried++
			}
			continue
		}
		byApp, rootReadme := c.repoReadmePaths(ctx, src)
		for _, a := range apps {
			if tried >= limit {
				break
			}
			tried++
			st.At[a.Appname] = now.Unix()
			d := pickCandidate(list, a.Appname)
			if d == nil {
				continue
			}
			// README 托管：先落盘（网络/磁盘 IO 不进索引锁），再在锁内只补三个协议字段。
			rp := byApp[strings.ToLower(a.Appname)]
			if rp == "" {
				rp = rootReadme
			}
			readmeURL := ""
			if a.ReadmeURL == "" && c.ensureReadme(ctx, a.Appname, src, rp) {
				readmeURL = c.base + "/moo/" + a.Appname + "/README.md"
			}
			// 0.2.73-fix（B-1）：必须走持写锁的字段级更新。
			// put() 要求调用方已持锁，而本函数跑在后台 goroutine，直接 put 会在无锁下写
			// doc.Apps（与 HTTP 侧 list() 并发 → Go 并发 map 属 fatal error），且用 list()
			// 的陈旧快照整体覆盖会抹掉期间写入的检查状态。updateFields 在锁内改活条目。
			changed, err := c.idx.updateFields(a.Appname, func(live *indexedApp) bool {
				dirty := false
				if live.Changelog == "" && d.Changelog != "" {
					live.Changelog = d.Changelog
					dirty = true
				}
				if len(live.Releases) == 0 {
					rels := map[string]*appRelease{}
					if d.Version != "" {
						node := &appRelease{Changelog: d.Changelog, UpdatedAt: d.UpdatedAt}
						if pkgs := currentPackages(live, d); len(pkgs) > 0 {
							node.Packages = pkgs
						}
						rels[d.Version] = node
					}
					if d.PrevVer != "" && d.PrevVer != d.Version {
						rels[d.PrevVer] = &appRelease{Changelog: d.PrevNotes, UpdatedAt: d.PrevAt, Packages: d.PrevPkgs}
					}
					if len(rels) > 0 {
						live.Releases = rels
						dirty = true
					}
				}
				if live.ReadmeURL == "" && readmeURL != "" {
					live.ReadmeURL = readmeURL
					dirty = true
				}
				return dirty
			})
			if err == nil && changed {
				filled++
			}
		}
	}
	c.saveBackfillState(st)
	return filled
}

// currentPackages 用条目已有的下载信息构造当前版本节点；版本不一致时不猜（返回空）。
func currentPackages(a *indexedApp, d *discoveredApp) map[string]*appPackage {
	if a.Version != "" && d.Version != "" && a.Version != d.Version {
		return nil
	}
	out := map[string]*appPackage{}
	for arch, p := range a.Packages {
		if p != nil {
			out[arch] = p
		}
	}
	return out
}
