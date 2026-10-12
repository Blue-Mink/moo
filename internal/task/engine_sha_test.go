package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// B5：sha256 投毒纠正路径——候选 1 错字节 + 源声明 sha → 删文件试候选 2
// 成功；OnFail 恰 1 次且 stage=sha256（日志取证点）。
func TestEngineDownloadSha256Correction(t *testing.T) {
	good := []byte("good package content 1234567890")
	bad := []byte("GOOD package content 1234567890") // 首字节不同
	sum := sha256.Sum256(good)
	wantHex := hex.EncodeToString(sum[:])

	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bad)
	}))
	defer srvA.Close()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(good)
	}))
	defer srvB.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "pkg.fpk")
	e := NewEngine("") // 无 aria2 → 内置单连接
	var fails []string
	err := e.Download(context.Background(), DownloadOpts{
		DestPath:   dest,
		Candidates: []string{srvA.URL + "/a.fpk", srvB.URL + "/b.fpk"},
		SHA256:     wantHex,
		OnFail: func(candidate, stage string, derr error) {
			fails = append(fails, stage)
		},
	})
	if err != nil {
		t.Fatalf("候选 2 应纠正成功: %v", err)
	}
	if len(fails) != 1 || fails[0] != "sha256" {
		t.Fatalf("OnFail 应恰 1 次且 stage=sha256: %v", fails)
	}
	got, rerr := os.ReadFile(dest)
	if rerr != nil || string(got) != string(good) {
		t.Fatalf("成品应为候选 2 内容: %v", rerr)
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Error(".part 残留应被清除")
	}
}

// 全部候选 sha 都不匹配 → 全部失败错误 + 每候选一次 sha256 OnFail。
func TestEngineDownloadSha256AllFail(t *testing.T) {
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("wrong a"))
	}))
	defer srvA.Close()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("wrong b"))
	}))
	defer srvB.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "pkg2.fpk")
	e := NewEngine("")
	sum := sha256.Sum256([]byte("never served"))
	var fails []string
	err := e.Download(context.Background(), DownloadOpts{
		DestPath:   dest,
		Candidates: []string{srvA.URL + "/a.fpk", srvB.URL + "/b.fpk"},
		SHA256:     hex.EncodeToString(sum[:]),
		OnFail:     func(candidate, stage string, derr error) { fails = append(fails, stage) },
	})
	if err == nil {
		t.Fatal("全候选 sha 不匹配应失败")
	}
	if len(fails) != 2 || fails[0] != "sha256" || fails[1] != "sha256" {
		t.Fatalf("每候选各 1 次 sha256 OnFail: %v", fails)
	}
	if _, rerr := os.Stat(dest); !os.IsNotExist(rerr) {
		t.Error("失败成品应被删除")
	}
}
