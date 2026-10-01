// Engine：统一 FPK 下载引擎。
//
// 三条下载路径（「下载 FPK」任务 / 安装 / 升级）共用：
//   - aria2 多连接（-x16 -s16）：快镜像上 +30–50%，直连抽风时自动兜住掐线；
//   - 候选 URL 依次试错（最佳镜像在前，直连兜底）；
//   - 断点续传：aria2 用 .aria2 控制文件，内置单连接用 .part（Range）。
//
// 2026-09-22 测试机实测：GitHub 直连间歇 0B/s↔13.5MB/s；hk.gh-proxy 单连接
// 57MB/s、aria2×16 达 82MiB/s（15/16 连接存活）；wget.la 延迟过线仅 139KB/s。
package task

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"moo/internal/netguard"
)

// Engine 可复用下载引擎（任务管理器与安装管线共享同一实例）。
type Engine struct {
	Aria2Bin string // 空 = aria2 不可用，全部走内置单连接
	Aria2Conn int   // aria2 并行连接/分片数
	// MinAria2Bytes：已知大小低于该值时跳过 aria2（小文件无多连接收益，
	// 还浪费镜像的并行连接额度——镜像会掐部分并行连接）。
	MinAria2Bytes int64

	client *http.Client
}

// NewEngine 创建引擎；aria2Bin 为空表示自动探测失败/显式禁用。
func NewEngine(aria2Bin string) *Engine {
	return &Engine{
		Aria2Bin:      aria2Bin,
		Aria2Conn:     16,
		MinAria2Bytes: 2 * 1024 * 1024,
		client:        &http.Client{}, // 不设超时：大文件下载
	}
}

// DetectAria2 寻找可用 aria2c（显式配置 > fnOS 内置 > 通用路径；override="off" 禁用）。
func DetectAria2(override string) string {
	if override == "off" {
		return ""
	}
	var cands []string
	if override != "" {
		cands = append(cands, override)
	}
	cands = append(cands,
		"/usr/trim/bin/trim-aria2c", // fnOS 内置（aria2 1.36）
		"/usr/bin/aria2c",
		"/usr/local/bin/aria2c",
	)
	for _, p := range cands {
		if p == "" {
			continue
		}
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// DownloadOpts 描述一次下载。
type DownloadOpts struct {
	DestPath   string   // 成品最终路径（成功后完整文件在此）
	Candidates []string // 候选 URL（偏好顺序；任一成功即返回）
	SizeBytes  int64    // 0 = 未知；已知时用于成品大小校验与 aria2 决策
	// SHA256 源声明的包哈希（小写 hex）；非空时下载成品校验，不匹配即丢弃。
	SHA256   string
	Progress func(done, size int64)
	// OnFail 每个候选（含 aria2 阶段）失败时的回调，可选（日志/SSE 提示）。
	OnFail func(candidate, stage string, err error)
}

// Download 下载到完成：候选按序尝试，每个候选内先 aria2（如适用）再内置单连接。
// ctx 取消（暂停）时返回 ctx.Err()，已写部分保留供续传。
//
// 安全（2026-09-27 审核）：候选 URL 来自源数据（任意发布者可控），
// 下载前先过 netguard.Public（scheme 限定 http/https + 主机须为公共地址）
// ——既挡 aria2 选项注入（--exec=… 这类以 - 开头的值过不了 URL 校验），
// 也挡 SSRF（内网/元数据地址）。源声明了 sha256 时，下载成品逐一校验，
// 不匹配的候选丢弃文件继续试下一个（镜像投毒可被干净镜像纠正）。
func (e *Engine) Download(ctx context.Context, o DownloadOpts) error {
	if len(o.Candidates) == 0 {
		return fmt.Errorf("无下载候选")
	}
	var errs []string
	for i, u := range o.Candidates {
		if _, gerr := netguard.Public(u); gerr != nil {
			errs = append(errs, fmt.Sprintf("%s: 地址被安全策略拒绝: %v", u, gerr))
			if o.OnFail != nil {
				o.OnFail(u, fmt.Sprintf("候选%d/%d", i+1, len(o.Candidates)), gerr)
			}
			continue
		}
		if err := e.tryOne(ctx, o, u); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			errs = append(errs, fmt.Sprintf("%s: %v", u, err))
			if o.OnFail != nil {
				o.OnFail(u, fmt.Sprintf("候选%d/%d", i+1, len(o.Candidates)), err)
			}
			continue
		}
		if o.SHA256 != "" {
			if herr := e.checkSHA256(o); herr != nil {
				_ = os.Remove(o.DestPath)
				_ = os.Remove(o.DestPath + ".part")
				errs = append(errs, fmt.Sprintf("%s: %v", u, herr))
				if o.OnFail != nil {
					o.OnFail(u, "sha256", herr)
				}
				continue
			}
		}
		return nil
	}
	return fmt.Errorf("全部候选失败: %s", strings.Join(errs, " | "))
}

// checkSHA256 校验下载成品与源声明哈希（大小写不敏感；空声明=跳过）。
func (e *Engine) checkSHA256(o DownloadOpts) error {
	f, err := os.Open(o.DestPath)
	if err != nil {
		return fmt.Errorf("成品校验失败: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("成品校验失败: %w", err)
	}
	got := strings.ToLower(hex.EncodeToString(h.Sum(nil)))
	want := strings.ToLower(strings.TrimSpace(o.SHA256))
	if got != want {
		return fmt.Errorf("安装包 sha256 不匹配（期望 %s…，实际 %s…）", want[:8], got[:8])
	}
	return nil
}

// tryOne 单个候选：aria2（如适用）失败则回退同 URL 内置单连接。
func (e *Engine) tryOne(ctx context.Context, o DownloadOpts, u string) error {
	if e.Aria2Bin != "" && (o.SizeBytes <= 0 || o.SizeBytes >= e.MinAria2Bytes) {
		err := e.runAria2(ctx, o, u)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err() // 暂停：不试下一个候选
		}
		if o.OnFail != nil {
			o.OnFail(u, "aria2", err)
		}
	}
	return e.runHTTP(ctx, o, u)
}

var aria2ProgressRe = regexp.MustCompile(`\[(?:#[0-9a-f]+|[^#\s]+) ([\d.]+)([KMG]iB)/([\d.]+)([KMG]iB)\(`)

// 单位换算（aria2 输出 MiB 等二进制单位）。
var aria2Unit = map[string]int64{"KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30}

// runAria2 以 CLI 方式跑 aria2c 下载 o.DestPath（候选 u）。
// 暂停 = SIGTERM（aria2 保存 .aria2 控制文件，可续传），5s 后升级 SIGKILL；
// 进度 = 解析 --summary-interval 输出行。
//
// 注意：io.Pipe 的 EOF 只能来自 pw.Close()（子进程退出不会自动关 pw），
// 因此必须由 Wait 完成后显式关 pw，否则解析 goroutine 永久阻塞。
func (e *Engine) runAria2(ctx context.Context, o DownloadOpts, u string) error {
	// 单 URI 用 CLI 参数（-o 绝对落盘名 + URL 位置参数），不用输入文件——
	// 输入文件里 out= 的绝对路径在部分 aria2 版本会被按 CWD 相对解析（2026-09-22
	// 实测 aria2 1.36：out=/tmp/x → 落到 <CWD>/tmp/x）。
	// .aria2 控制文件自动伴随 dest，暂停/续传依赖它。
	args := []string{
		fmt.Sprintf("-x%d", e.Aria2Conn),
		fmt.Sprintf("-s%d", e.Aria2Conn),
		"--max-tries=2",
		"--timeout=20",
		"--connect-timeout=15",
		"--summary-interval=2",
		"--allow-overwrite=true",
		"-o", o.DestPath,
		u,
	}
	cmd := exec.Command(e.Aria2Bin, args...) // 不用 CommandContext：暂停要 SIGTERM 保存续传状态
	var outBuf syncBuffer
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		pw.Close()
		return fmt.Errorf("启动 aria2 失败: %w", err)
	}

	// 解析进度/错误输出
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if m := aria2ProgressRe.FindStringSubmatch(line); m != nil {
				if done, ok := parseAria2Size(m[1], m[2]); ok {
					if total, ok2 := parseAria2Size(m[3], m[4]); ok2 && total > 0 {
						if o.Progress != nil {
							o.Progress(done, total)
						}
					}
				}
			} else if outBuf.Len() < 64*1024 && !strings.HasPrefix(line, "[#") {
				outBuf.WriteString(line + "\n") // 保留错误输出尾部
			}
		}
	}()

	// Wait 完成（子进程退出 + 内部拷贝结束）后关 pw 给出 EOF
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
		pw.Close()
	}()

	// ctx 取消（暂停）：SIGTERM 让 aria2 保存续传状态，5s 未退再 SIGKILL
	go func() {
		<-ctx.Done()
		if p := cmd.Process; p != nil {
			_ = p.Signal(syscall.SIGTERM)
			go func() {
				time.Sleep(5 * time.Second)
				_ = p.Kill()
			}()
		}
	}()

	err := <-waitCh
	pr.Close()
	if ctx.Err() != nil {
		return ctx.Err() // 暂停：.aria2 控制文件 + 已写部分保留
	}
	if err != nil {
		tail := strings.TrimSpace(outBuf.String())
		if len(tail) > 300 {
			tail = "…" + tail[len(tail)-300:]
		}
		return fmt.Errorf("aria2 退出码 %v: %s", err, tail)
	}
	// aria2 成功后已删除 .aria2 控制文件；校验成品
	if err := checkDest(o.DestPath, o.SizeBytes); err != nil {
		return err
	}
	return nil
}

// syncBuffer 线程安全 strings.Builder（解析 goroutine 写、主 goroutine 读）。
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

func (b *syncBuffer) WriteString(s string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.WriteString(s)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func parseAria2Size(v, unit string) (int64, bool) {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, false
	}
	mul, ok := aria2Unit[unit]
	if !ok {
		return 0, false
	}
	return int64(f * float64(mul)), true
}

// checkDest 校验成品存在且非空。
// 不做声明大小比对：源目录 size 字段单位/精度不可靠（2026-09-22 实测同源
// 混用十进制 MB 与 MiB），误判会丢弃已成功下载并触发整包重下。
// 完整性由传输层保证（HTTP ContentLength 全程校验 / aria2 分片计数）。
func checkDest(dest string, _ int64) error {
	fi, err := os.Stat(dest)
	if err != nil {
		return fmt.Errorf("成品不存在: %w", err)
	}
	if fi.Size() <= 0 {
		return fmt.Errorf("成品为空: %s", dest)
	}
	return nil
}

// runHTTP 内置单连接下载（.part 断点续传，Range）。
func (e *Engine) runHTTP(ctx context.Context, o DownloadOpts, u string) error {
	part := o.DestPath + ".part"

	offset := int64(0)
	if info, err := os.Stat(part); err == nil {
		offset = info.Size()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := e.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	defer resp.Body.Close()

	// 真实总量（ContentLength）优先于源声明值（声明单位/精度不可靠）
	actualTotal := o.SizeBytes
	switch {
	case offset > 0 && resp.StatusCode == http.StatusPartialContent:
		if resp.ContentLength > 0 {
			actualTotal = offset + resp.ContentLength
		}
		e.notify(o, offset, actualTotal)
	case offset > 0:
		offset = 0 // 服务器不支持 Range → 从头开始
		if resp.ContentLength > 0 {
			actualTotal = resp.ContentLength
		}
		e.notify(o, 0, actualTotal)
	case resp.StatusCode == http.StatusOK:
		if resp.ContentLength > 0 {
			actualTotal = resp.ContentLength
		}
		e.notify(o, 0, actualTotal)
	default:
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			return err
		}
	}

	buf := make([]byte, 256*1024)
	written := offset
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return werr
			}
			written += int64(n)
			e.notify(o, written, actualTotal)
		}
		if rerr == io.EOF {
			f.Close()
			if err := os.Rename(part, o.DestPath); err != nil {
				return fmt.Errorf("落盘改名失败: %w", err)
			}
			return checkDest(o.DestPath, o.SizeBytes)
		}
		if rerr != nil {
			f.Close()
			if ctx.Err() != nil {
				return ctx.Err() // 暂停：已写部分保留在 .part
			}
			return rerr
		}
	}
}

// notify 转发进度（size 未知时保持 0）。
func (e *Engine) notify(o DownloadOpts, done, size int64) {
	if o.Progress == nil {
		return
	}
	o.Progress(done, size)
}

