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
	"time"
)

// defaultIndexGz 出厂默认应用索引（0.2.35+）：由 gen_default_index.py 从生产
// index.json 生成，gzip 压缩后约 120KB（JSON 约 700KB）。
//
// 为什么内置：mate 以前是纯聚合器 —— 新装/离线时索引从 0 开始，软仓不可达就是
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
