package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// E2E（2026-11-21 FnDepot#24）：真实下载路径下，镜像壳候选失败后必须回落到
// 直连并成功；失败信息要指明「加速源」，而不是把锅甩给源站。
// 反向验证：把 HTTPHintFor 还原成 HTTPHint(code)，本用例第 2 段断言必 FAIL。
func TestDownloadE2E24_DeadMirrorFallsBackToDirect(t *testing.T) {
	payload := []byte(strings.Repeat("FPKDATA-", 512))
	sum := sha256.Sum256(payload)
	wantHex := hex.EncodeToString(sum[:])

	alive := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer alive.Close()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Fastly error: unknown domain cdn.gh-proxy.org", http.StatusInternalServerError)
	}))
	defer dead.Close()

	direct := alive.URL + "/fnclearup.fpk"
	wrapped := dead.URL + "/" + direct // 镜像壳：<dead>/<direct>

	t.Run("多候选：壳失败→直连成功", func(t *testing.T) {
		dir := t.TempDir()
		dest := filepath.Join(dir, "pkg.fpk")
		var fails []string
		err := NewEngine("").Download(context.Background(), DownloadOpts{
			DestPath:   dest,
			Candidates: []string{wrapped, direct},
			SHA256:     wantHex,
			OnFail: func(c, stage string, derr error) {
				fails = append(fails, derr.Error())
			},
		})
		if err != nil {
			t.Fatalf("应回落到直连成功，实际 %v", err)
		}
		got, _ := os.ReadFile(dest)
		if string(got) != string(payload) {
			t.Fatalf("成品内容不符（%d 字节）", len(got))
		}
		if len(fails) != 1 {
			t.Fatalf("应恰 1 条失败记录，实际 %v", fails)
		}
		if !strings.Contains(fails[0], "加速源故障") {
			t.Errorf("镜像壳 500 的失败信息应含「加速源故障」，实际 %q", fails[0])
		}
		if strings.Contains(fails[0], "源站故障") {
			t.Errorf("不应误报源站故障，实际 %q", fails[0])
		}
	})

	t.Run("单候选（旧行为）：整串错误要指出加速源", func(t *testing.T) {
		dir := t.TempDir()
		err := NewEngine("").Download(context.Background(), DownloadOpts{
			DestPath:   filepath.Join(dir, "pkg.fpk"),
			Candidates: []string{wrapped},
		})
		if err == nil {
			t.Fatal("单条死镜像候选必须失败（不能静默成功）")
		}
		msg := err.Error()
		if !strings.Contains(msg, "加速源故障") {
			t.Errorf("错误信息应指明加速源故障，实际 %q", msg)
		}
		if strings.Contains(msg, "源站故障") {
			t.Errorf("不应误报源站故障，实际 %q", msg)
		}
		if !strings.Contains(msg, "加速镜像这一跳") {
			t.Errorf("结论应提示停在加速镜像这一跳，实际 %q", msg)
		}
	})
}
