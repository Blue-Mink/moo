package api

// 0.6.267：resolveMooLogPath（安装向导日志路径解析）
// .logpath 由 install_callback 写入（printf '%s\n'，带尾换行），
// start_daemon 的 shell 侧按同一规则解析——缺省回退 dataDir/moo.log。

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveMooLogPath(t *testing.T) {
	dir := t.TempDir()

	// 1. 无 .logpath → 默认 dataDir/moo.log
	if got := resolveMooLogPath(dir); got != filepath.Join(dir, "moo.log") {
		t.Fatalf("无 .logpath: got %q", got)
	}

	// 2. 相对路径（带尾换行，模拟 printf '%s\n'）→ 相对数据目录
	if err := os.WriteFile(filepath.Join(dir, ".logpath"), []byte("moo.log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resolveMooLogPath(dir); got != filepath.Join(dir, "moo.log") {
		t.Fatalf("相对 moo.log: got %q", got)
	}

	// 3. 相对子目录
	if err := os.WriteFile(filepath.Join(dir, ".logpath"), []byte("logs/app.log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resolveMooLogPath(dir); got != filepath.Join(dir, "logs", "app.log") {
		t.Fatalf("相对子目录: got %q", got)
	}

	// 4. 绝对路径原样使用
	if err := os.WriteFile(filepath.Join(dir, ".logpath"), []byte("/var/log/custom/moo.log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resolveMooLogPath(dir); got != "/var/log/custom/moo.log" {
		t.Fatalf("绝对路径: got %q", got)
	}

	// 5. 空白文件 → 回退默认
	if err := os.WriteFile(filepath.Join(dir, ".logpath"), []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resolveMooLogPath(dir); got != filepath.Join(dir, "moo.log") {
		t.Fatalf("空白文件: got %q", got)
	}

	// 6. 轮转跟解析路径走：自定义路径 >5MB 时归档到 <自定义路径>.1
	sub := t.TempDir()
	abs := filepath.Join(sub, "custom", "run.log")
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, ".logpath"), []byte(abs+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	big := make([]byte, logRotateMaxSize+1)
	if err := os.WriteFile(abs, big, 0o600); err != nil {
		t.Fatal(err)
	}
	rotateMooLog(sub)
	if fi, err := os.Stat(abs); err != nil || fi.Size() != 0 {
		t.Fatalf("自定义日志未被截断: %v %v", fi, err)
	}
	if fi, err := os.Stat(abs + ".1"); err != nil || fi.Size() != int64(len(big)) {
		t.Fatalf("归档缺失: %v %v", fi, err)
	}
}
