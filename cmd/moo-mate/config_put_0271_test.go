package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConfigPutSkipsUnchangedWrite：0.2.71 修复——PUT /api/config 提交与当前一致的内容
// 时不得写盘。前端打开设置页回填齿轮选择器会派发 scroll 事件，旧版因此每次都 PUT 一次
// 相同值，后端旧实现无条件 save（无意义写盘 + 刷文件时间）。
func TestConfigPutSkipsUnchangedWrite(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	c := &companion{
		cfgPath:  cfgPath,
		cfg:      &mateConfig{SyncInterval: "1h", LogLines: 200},
		syncWake: make(chan struct{}, 1),
	}
	if err := c.cfg.save(cfgPath); err != nil {
		t.Fatalf("初始写盘: %v", err)
	}

	put := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(body))
		c.handleConfigPut(rec, req)
		return rec
	}

	// 哨兵：手工把文件写成 save() 绝不会产出的内容。若请求写了盘，哨兵会被覆盖。
	sentinel := []byte(`{"marker":"sentinel"}`)
	if err := os.WriteFile(cfgPath, sentinel, 0o600); err != nil {
		t.Fatalf("写哨兵: %v", err)
	}

	// 1) 相同值 → 不写盘，回 200 且 unchanged=true
	rec := put(`{"sync_interval":"1h"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("同值 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	if resp["unchanged"] != true {
		t.Fatalf("同值未标记 unchanged：%s", rec.Body.String())
	}
	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("读回: %v", err)
	}
	if string(got) != string(sentinel) {
		t.Fatalf("同值请求仍写了盘（哨兵被覆盖）：%s", got)
	}

	// 2) 不同值 → 必须写盘，且哨兵被真实配置覆盖
	rec = put(`{"sync_interval":"2h"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("异值 status=%d body=%s", rec.Code, rec.Body.String())
	}
	got, err = os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("读回: %v", err)
	}
	if string(got) == string(sentinel) {
		t.Fatalf("异值请求没有写盘（哨兵仍在）")
	}
	if !strings.Contains(string(got), `"sync_interval": "2h"`) {
		t.Fatalf("落盘内容不含新值：%s", got)
	}
	if c.cfg.SyncInterval != "2h" {
		t.Fatalf("内存配置未更新：%q", c.cfg.SyncInterval)
	}

	// 3) 非法值仍要 400，且不得写盘
	if err := os.WriteFile(cfgPath, sentinel, 0o600); err != nil {
		t.Fatalf("重置哨兵: %v", err)
	}
	rec = put(`{"sync_interval":"nonsense"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法值 status=%d，期望 400；body=%s", rec.Code, rec.Body.String())
	}
	if got, _ = os.ReadFile(cfgPath); string(got) != string(sentinel) {
		t.Fatalf("非法值请求写盘了：%s", got)
	}
}
