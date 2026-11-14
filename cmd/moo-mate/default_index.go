package main

import (
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// defaultIndexGz 出厂默认应用索引（0.2.35+）：由 gen_default_index.py 从生产
// index.json 生成，gzip 压缩后约 120KB（JSON 约 700KB）。
//
// 为什么内置：mate 以前是纯聚合器 —— 新装/离线时索引从 0 开始，上游不可达就是
// 一个空应用列表。内置快照让「装完即有内容」，网络恢复后正常同步覆盖。
//
//go:embed default_index.json.gz
var defaultIndexGz []byte

// defaultSeedApps 仅供测试/日志：快照条数（解析失败返回 0）。
func defaultSeedApps() int {
	d, err := decodeDefaultIndex()
	if err != nil {
		return 0
	}
	return len(d.Apps)
}

func decodeDefaultIndex() (*indexDoc, error) {
	zr, err := gzip.NewReader(bytes.NewReader(defaultIndexGz))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	// 上限 32MB：快照应远小于此，超了说明文件被换掉
	data, err := io.ReadAll(io.LimitReader(zr, 32<<20))
	if err != nil {
		return nil, err
	}
	var d indexDoc
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, err
	}
	if d.Apps == nil {
		d.Apps = map[string]*indexedApp{}
	}
	return &d, nil
}

// fetchRemoteDefaultIndex 拉取远程「出厂默认应用列表」（0.2.37）。
// 失败返回错误，由调用方回落到内置快照 —— 远程清单不可达不能挡住启动。
func fetchRemoteDefaultIndex(ctx context.Context, client *http.Client, url string) (*indexDoc, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "moo-mate/"+buildVersion)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d（%d 字节）", resp.StatusCode, len(body))
	}
	var d indexDoc
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("解析失败：%w", err)
	}
	if len(d.Apps) == 0 {
		return nil, fmt.Errorf("清单为空")
	}
	return &d, nil
}

// writeDefaultIndex 原子写入默认索引（临时文件 + rename），返回条数。
func writeDefaultIndex(path string, d *indexDoc) (int, error) {
	body, err := json.MarshalIndent(d, "", " ")
	if err != nil {
		return 0, err
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return 0, err
		}
	}
	tmp := path + ".seed"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return len(d.Apps), nil
}

// bootstrapDefaultIndex 在索引文件**不存在**时写入默认列表。
//
// 0.2.37：先读远程清单（remoteURL，测试期可指向本地 Gitea），失败回落内置快照。
// 内置快照仍随包发布 —— 离线/远程不可达时「装完即有内容」的行为保持不变。
//
// 边界（重要）：
//   - 文件已存在（哪怕是空文件）→ 一律不动。升级/已有数据不覆盖的前提。
//   - 远程/内置都失败 → 静默跳过，回落空索引（不能因为清单坏了就不启动）。
//
// 返回 (写入条数, 来源 "remote"/"embedded", 错误)。
func bootstrapDefaultIndex(path, remoteURL string, client *http.Client) (int, string, error) {
	if _, err := os.Stat(path); err == nil {
		return 0, "", nil
	} else if !os.IsNotExist(err) {
		return 0, "", err
	}
	if remoteURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if d, err := fetchRemoteDefaultIndex(ctx, client, remoteURL); err != nil {
			logf("远程默认列表不可用（回落内置快照）：%v", err)
		} else if n, werr := writeDefaultIndex(path, d); werr != nil {
			return 0, "", werr
		} else {
			return n, "remote", nil
		}
	}
	d, err := decodeDefaultIndex()
	if err != nil {
		return 0, "", err
	}
	n, err := writeDefaultIndex(path, d)
	if err != nil {
		return 0, "", err
	}
	return n, "embedded", nil
}

// indexClearedFlag 用户主动「清除同步索引」留下的标记（数据目录下）。
// 存在 = 索引是用户主动清空的，启动补漏不得把出厂默认清单塞回去。
const indexClearedFlag = ".index-cleared"

// markClearedByUser 记录「本机索引由用户主动清空」。
func (ix *index) markClearedByUser() error {
	p := filepath.Join(filepath.Dir(ix.path), indexClearedFlag)
	return os.WriteFile(p, []byte(time.Now().Format(time.RFC3339)+"\n"), 0o600)
}

// clearUserClearedFlag 用户重新新增应用后清标记，恢复「空索引补默认清单」的行为。
func (ix *index) clearUserClearedFlag() {
	_ = os.Remove(filepath.Join(filepath.Dir(ix.path), indexClearedFlag))
}

// userClearedIndex 是否「用户主动清空且此后未新增应用」。
func (ix *index) userClearedIndex() bool {
	_, err := os.Stat(filepath.Join(filepath.Dir(ix.path), indexClearedFlag))
	return err == nil
}

// seedDefaultIfEmpty 空索引兜底：写入出厂默认清单（远端优先，失败回落内置快照）。
//
// 为什么需要：bootstrapDefaultIndex 只在 index.json **不存在**时执行，而历史版本
// 可能把索引清成空文件；此后 bootstrap 因文件已存在而跳过 → 列表永远为空，
// 且旧版没有任何恢复入口。
//
// 边界：已有条目一律不动（不覆盖用户数据）；用户主动「清除同步索引」留了标记则
// 跳过（否则清了重启又被塞回默认应用，清除语义就失效）。
// 返回 (写入条数, 来源 remote|embedded|skipped-user-cleared, 错误)。
func (ix *index) seedDefaultIfEmpty(remoteURL string, client *http.Client) (int, string, error) {
	ix.mu.RLock()
	empty := len(ix.doc.Apps) == 0
	ix.mu.RUnlock()
	if !empty {
		return 0, "", nil
	}
	if ix.userClearedIndex() {
		return 0, "skipped-user-cleared", nil
	}
	if remoteURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if d, err := fetchRemoteDefaultIndex(ctx, client, remoteURL); err != nil {
			logf("远程默认列表不可用（回落内置快照）：%v", err)
		} else {
			return ix.seedFrom(d, "remote")
		}
	}
	d, err := decodeDefaultIndex()
	if err != nil {
		return 0, "", err
	}
	return ix.seedFrom(d, "embedded")
}

// seedFrom 过滤掉用户主动删除过的条目后再写入空索引。
// 过滤后为空则**不写盘**：此时若照写一个 0 条索引，等于把「用户删光了」变成
// 「索引里有 0 条但排除记录还在」，下次同步仍然什么都不加，语义反而更绕。
func (ix *index) seedFrom(d *indexDoc, source string) (int, string, error) {
	var apps map[string]*indexedApp
	if d != nil {
		apps = ix.filterRemovedDefaults(d.Apps)
	}
	if len(apps) == 0 {
		return 0, "all-removed", nil
	}
	n, err := ix.replaceApps(apps)
	if err != nil {
		return 0, "", err
	}
	return n, source, nil
}

// filterRemovedDefaults 剔除用户主动删除/清除过的条目（不修改入参）。
func (ix *index) filterRemovedDefaults(apps map[string]*indexedApp) map[string]*indexedApp {
	removed := ix.removedSet()
	out := make(map[string]*indexedApp, len(apps))
	for name, a := range apps {
		if a == nil || removed[name] {
			continue
		}
		out[name] = a
	}
	return out
}

// defaultIndexKeys 返回出厂默认清单里的应用名（只读嵌入副本，不联网）。
func defaultIndexKeys() []string {
	d, err := decodeDefaultIndex()
	if err != nil || d == nil {
		return nil
	}
	keys := make([]string, 0, len(d.Apps))
	for name := range d.Apps {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	return keys
}

// clearByUserAll 用户点「清除同步索引」：清空索引，同时把出厂默认清单的 key
// 一并记入排除名单，保证重启兜底 / 「同步更新」补全都不会再把它们塞回来。
func (ix *index) clearByUserAll() error {
	return ix.clearByUser(defaultIndexKeys())
}

// mergeDefaultList 把「出厂默认应用列表」里索引**尚未收录**的条目补进来（只补不改）。
//
// 与 seedDefaultIfEmpty 的分工：后者只在索引为空时整体写入（启动兜底）；
// 本函数供「同步更新」按钮每次调用，用于远端清单新增应用的场景。
//
// 三条铁律（用户 2026-11-12 明确要求）：
//  1. 已存在的 key 一律跳过 —— 用户自己添加的、版本已被更新的，绝不覆盖，互不影响；
//  2. 用户主动删除过的 key（removed_defaults）跳过 —— 删掉的不能又自己冒出来；
//  3. 用户点过「清除同步索引」（标记在）整体跳过 —— 清除语义优先于补全。
func (ix *index) mergeDefaultList(remoteURL string, client *http.Client) (added int, source string, err error) {
	if ix.userClearedIndex() {
		return 0, "skipped-user-cleared", nil
	}
	var d *indexDoc
	source = "embedded"
	if remoteURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if rd, rerr := fetchRemoteDefaultIndex(ctx, client, remoteURL); rerr != nil {
			logf("远程默认列表不可用（回落内置快照）：%v", rerr)
		} else {
			d, source = rd, "remote"
		}
	}
	if d == nil {
		ed, eerr := decodeDefaultIndex()
		if eerr != nil {
			return 0, "", eerr
		}
		d = ed
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	removed := make(map[string]bool, len(ix.doc.RemovedDefaults))
	for _, v := range ix.doc.RemovedDefaults {
		removed[v] = true
	}
	for name, a := range d.Apps {
		if a == nil {
			continue
		}
		if _, exists := ix.doc.Apps[name]; exists {
			continue // 已收录：只补不改
		}
		if removed[name] {
			continue // 用户删过：不复活
		}
		if a.Appname == "" {
			a.Appname = name
		}
		ix.doc.Apps[name] = a
		added++
	}
	if added == 0 {
		return 0, source, nil
	}
	return added, source, ix.save()
}

// replaceApps 用给定集合替换索引内容并落盘（仅供空索引兜底使用）。
func (ix *index) replaceApps(apps map[string]*indexedApp) (int, error) {
	if apps == nil {
		apps = map[string]*indexedApp{}
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.doc.Apps = apps
	if err := ix.save(); err != nil {
		return 0, err
	}
	return len(apps), nil
}
