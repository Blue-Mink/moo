package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"aead.dev/minisign"
)

const (
	maxFPKSize     = 64 << 20 // 发布包上限（当前 ~5MB，留足余量）
	maxSidecarSize = 64 << 10
)

var reHex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ErrTransport 标记「通道不可达/超时」——按设计不算契约违规（不算发布失败），
// 与「通道可达但缺件/摘要不符/验签失败」严格区分，否则国内出口一抖门就红。
var ErrTransport = errors.New("通道不可达")

// Artifact 是下载并通过全部校验的产物。
type Artifact struct {
	Path   string   `json:"path"`
	Size   int64    `json:"size"`
	SHA256 string   `json:"sha256"`
	FPK    *FPKInfo `json:"fpk"`
}

// ParseSHA256Sidecar 解析 `.sha256` 侧车内容（`<64hex>[ 文件名]`，取首字段）。
func ParseSHA256Sidecar(text string) (string, error) {
	fields := strings.Fields(strings.ToLower(text))
	if len(fields) == 0 {
		return "", errors.New(".sha256 内容为空")
	}
	if !reHex64.MatchString(fields[0]) {
		return "", fmt.Errorf(".sha256 首字段不是 64 位 hex：%q", truncate(fields[0], 16))
	}
	return fields[0], nil
}

// VerifyAssets 按客户端真实路径验收一条通道：
// 下载 FPK → 下载 .sha256 并比对内容 → 下载 .minisig 并用内嵌公钥验签 →
// 解包核对 manifest 版本/平台与二进制 ELF 架构。
//
// 任何一步失败即返回错误：这正是 0.6.329+ fail-closed 的等价判定。
func VerifyAssets(ctx context.Context, c *http.Client, a Assets, version, arch, workDir string) (*Artifact, error) {
	if c == nil {
		c = http.DefaultClient
	}
	if !a.Complete() {
		return nil, fmt.Errorf("三件套不完整，缺 %s", strings.Join(a.Missing(), "+"))
	}
	if strings.TrimSpace(workDir) == "" {
		d, err := os.MkdirTemp("", "moo-release-")
		if err != nil {
			return nil, err
		}
		workDir = d
		defer os.RemoveAll(d)
	} else if err := os.MkdirAll(workDir, 0o700); err != nil {
		return nil, err
	}

	name := FPKName(version, arch)
	dst := filepath.Join(workDir, name)
	size, sha, err := downloadFile(ctx, c, a.FPK, dst, maxFPKSize)
	if err != nil {
		return nil, fmt.Errorf("下载 FPK 失败: %w", err)
	}

	want, err := fetchSidecarSHA(ctx, c, a.SHA256)
	if err != nil {
		return nil, err
	}
	if want != sha {
		return nil, fmt.Errorf("sha256 不符：侧车 %s… != 实算 %s…", truncate(want, 12), truncate(sha, 12))
	}

	sig, err := fetchLimited(ctx, c, a.Minisig, maxSidecarSize)
	if err != nil {
		return nil, fmt.Errorf("下载 .minisig 失败: %w", err)
	}
	if err := verifySignatureFile(dst, sig); err != nil {
		return nil, err
	}

	info, err := InspectFPK(dst)
	if err != nil {
		return nil, fmt.Errorf("解包校验失败: %w", err)
	}
	if err := info.CheckAgainst(version, arch); err != nil {
		return nil, err
	}
	return &Artifact{Path: dst, Size: size, SHA256: sha, FPK: info}, nil
}

// verifySignatureFile 用内嵌公钥校验 path 的 minisign 分离签名。
//
// 这是全仓唯一一份「minisign 验签」实现：远程验收（VerifyAssets）与本地
// 验收（VerifyLocal）都走这里，避免两处规则漂移。公钥恒为 go:embed 的
// 固定公钥，**不联网取公钥**（否则攻击者能连公钥一起替换）。
func verifySignatureFile(path string, sig []byte) error {
	if len(sig) == 0 {
		return errors.New(".minisig 内容为空")
	}
	pk, err := pubkeyForVerify()
	if err != nil {
		return err
	}
	msg, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !minisign.Verify(pk, msg, sig) {
		return errors.New("minisign 验签失败（签名无效或不匹配，客户端会拒绝自更新）")
	}
	return nil
}

// fetchSidecarSHA 拉取并解析 .sha256 侧车。
func fetchSidecarSHA(ctx context.Context, c *http.Client, url string) (string, error) {
	b, err := fetchLimited(ctx, c, url, 4<<10)
	if err != nil {
		return "", fmt.Errorf("下载 .sha256 失败: %w", err)
	}
	return ParseSHA256Sidecar(string(b))
}

// fetchLimited 拉取小文件（上限 n 字节）。
func fetchLimited(ctx context.Context, c *http.Client, url string, n int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "moo-release-probe")
	req.Header.Set("Accept", "*/*")
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTransport, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
			return nil, fmt.Errorf("HTTP %d（资产不存在）", resp.StatusCode)
		}
		return nil, fmt.Errorf("%w: HTTP %d", ErrTransport, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, n+1))
}

// downloadFile 流式下载并同时算 sha256，返回 (字节数, 小写 hex)。
func downloadFile(ctx context.Context, c *http.Client, url, dst string, limit int64) (int64, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("User-Agent", "moo-release-probe")
	resp, err := c.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("%w: %v", ErrTransport, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
			return 0, "", fmt.Errorf("HTTP %d（资产不存在）", resp.StatusCode)
		}
		return 0, "", fmt.Errorf("%w: HTTP %d", ErrTransport, resp.StatusCode)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, "", err
	}
	h := sha256.New()
	n, cerr := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, limit+1))
	closeErr := f.Close()
	// 读响应体中断（客户端超时 / 对端断流 / 连接重置）属传输失败，不是契约违规：
	// 2026-10-30 真实误判——5MB 下载在 180s 超时后被报成「契约违规」。
	if cerr != nil {
		if isTransportErr(cerr) {
			return 0, "", fmt.Errorf("%w: 下载中断: %v", ErrTransport, cerr)
		}
		return 0, "", cerr
	}
	if closeErr != nil {
		return 0, "", closeErr
	}
	if n > limit {
		return 0, "", fmt.Errorf("文件超过上限 %d 字节", limit)
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

// isTransportErr 判定错误是否属「通道不可达」（超时/断流/连接重置）。
// 这类错误按设计不算契约违规——否则国内慢网会把好发布判成坏发布。
func isTransportErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrTransport) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.EOF) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	msg := err.Error()
	for _, s := range []string{"connection reset", "broken pipe", "unexpected EOF",
		"i/o timeout", "Client.Timeout", "connection refused", "no such host"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// verifyAssetsFn 是验收函数入口（测试可替换；正常路径即 VerifyAssets 本体）。
var verifyAssetsFn = VerifyAssets

// maxSameURLAttempts 是同一 FPK URL 的最大验收尝试次数。
// 四条兜底通道常常共用同一个 release URL：失败后允许再试一次（防瞬时断流），
// 之后改试别的 URL（如镜像），避免把同一下载重复 4 遍各等满超时。
const maxSameURLAttempts = 2

// skipReason 判定该通道本轮是否跳过验收，返回非空原因即跳过。
// 跳过都不算失败（也不算通过）：默认只验第一条可用通道、同一 URL 已验收通过、
// 同一 URL 已试满次数。
func skipReason(fpk string, done, verifyAll bool, verified map[string]bool, attempts map[string]int) string {
	if done && !verifyAll {
		return "默认只验收第一条可用通道（--verify-all 可全验）"
	}
	if verified[fpk] {
		return "同一 FPK URL 已验收通过，跳过重复下载"
	}
	if attempts[fpk] >= maxSameURLAttempts {
		return fmt.Sprintf("同一 FPK URL 已尝试验收 %d 次仍未通过，停止重试", maxSameURLAttempts)
	}
	return ""
}

// VerifyAll 对每条「探测成功」的通道做客户端同款验收（下载+哈希+验签+解包）。
// 探测失败的通道原样保留，不做二次尝试。
func VerifyAll(ctx context.Context, o Opts, rs []Result) []Result {
	o = o.normalize()
	out := make([]Result, len(rs))
	copy(out, rs)
	dlTimeout := o.VerifyTimeout
	if dlTimeout <= 0 {
		dlTimeout = 180 * time.Second
	}
	// 下载客户端：连接超时短（国内直连 GitHub 常是 TCP 层不通，30s 干等没意义），
	// 总超时长（经镜像 ~90KB/s，5MB 需 ~60s）。
	dlClient := &http.Client{
		Timeout: dlTimeout,
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second,
			ForceAttemptHTTP2:     true,
			Proxy:                 http.ProxyFromEnvironment,
		},
	}
	done := false
	verified := map[string]bool{} // 已通过验收的 FPK URL（只有成功才登记，失败不能污染去重）
	attempts := map[string]int{}  // 每个 FPK URL 的验收尝试次数
	for i := range out {
		if !out[i].OK {
			continue
		}
		if reason := skipReason(out[i].Assets.FPK, done, o.VerifyAll, verified, attempts); reason != "" {
			out[i].VerifySkipped = true
			out[i].VerifySkipReason = reason
			out[i].Extra = append(out[i].Extra, reason)
			continue
		}
		attempts[out[i].Assets.FPK]++
		var work string
		var err error
		if o.WorkDir != "" {
			if err = os.MkdirAll(o.WorkDir, 0o700); err != nil {
				out[i].VerifyErr = err.Error()
				continue
			}
			work = filepath.Join(o.WorkDir, "verify-"+out[i].Channel)
			os.RemoveAll(work)
			err = os.MkdirAll(work, 0o700)
		} else {
			work, err = os.MkdirTemp("", "moo-verify-")
		}
		if err != nil {
			out[i].VerifyErr = err.Error()
			continue
		}
		art, err := verifyAssetsFn(ctx, dlClient, out[i].Assets, o.Tag, o.Arch, work)
		os.RemoveAll(work)
		if err != nil {
			out[i].VerifyErr = err.Error()
			out[i].VerifyTransport = errors.Is(err, ErrTransport)
			continue
		}
		out[i].Artifact = art
		out[i].Verified = true
		verified[out[i].Assets.FPK] = true
		done = true
	}
	return out
}
