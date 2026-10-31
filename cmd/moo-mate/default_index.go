package main

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
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

// bootstrapDefaultIndex 在索引文件**不存在**时写入内置快照。
//
// 边界（重要）：
//   - 文件已存在（哪怕是空文件）→ 一律不动。升级/已有数据不覆盖的前提。
//   - 快照损坏 / 解压失败 → 静默跳过，回落空索引（不能因为内置数据坏了就不启动）。
//   - 写入用 临时文件 + rename（与 index.save 的原子写一致），避免被读到半截。
//
// 返回 (写入了多少条, 错误)；未写入时条数为 0。
func bootstrapDefaultIndex(path string) (int, error) {
	if _, err := os.Stat(path); err == nil {
		return 0, nil
	} else if !os.IsNotExist(err) {
		return 0, err
	}
	d, err := decodeDefaultIndex()
	if err != nil {
		return 0, err
	}
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
