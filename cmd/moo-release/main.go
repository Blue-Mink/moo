// Command moo-release 是 internal/release 的命令行入口。
//
// 用途：发布前后一键核对「五通道三件套」是否齐全。退出码语义固定，
// 便于 CI / 发布脚本直接判定：
//
//	0  —— 至少一条通道通过客户端同款验收，且没有「可达但校验失败」的通道
//	1  —— 全部不可达 / 有通道可达但三件套缺失或校验失败 / --strict 下未全绿
//	2  —— 参数错误
//
// 跳过验收（默认只验第一条、同 URL 重复）与传输不可达（限流/超时）都不算失败。
//
// 只读：本命令不发任何写请求、不下载 FPK 正文（最多读 1 字节探测）。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"moo/internal/release"
)

func main() {
	var (
		tag      = flag.String("tag", "", "目标版本号，如 0.6.377（必需）")
		arch     = flag.String("arch", "x86", "架构：x86 / arm")
		repo     = flag.String("repo", release.DefaultRepo, "GitHub owner/repo，逗号分隔可多个")
		only     = flag.String("channels", "", "只跑指定通道，逗号分隔（api,web302,selfidx,fndepot,mirror）")
		jsonOut  = flag.Bool("json", false, "输出 JSON（便于脚本消费）")
		timeout  = flag.Int("timeout", 20, "单请求超时（秒）")
		verbose  = flag.Bool("v", false, "打印每条通道的详细结果")
		verify   = flag.Bool("verify", true, "对探测成功的通道做客户端同款验收（下载+sha256+minisig+解包）")
		strict   = flag.Bool("strict", false, "要求全部通道通过（默认允许 api 通道因限流不可用）")
		workdir  = flag.String("workdir", "", "验收临时目录（默认系统临时目录，用完即删）")
		vtimeout = flag.Int("verify-timeout", 180, "真下载超时（秒；国内经镜像 5MB 约需 60s）")
		vall     = flag.Bool("verify-all", false, "验收所有探测通过的通道（默认只验第一条，模拟用户实际选择）")
		localDir = flag.String("local-dir", "", "只做本地验收：对目录里的 moo_<tag>_<arch>.fpk 三件套跑客户端同款校验（不联网）")
	)
	flag.Parse()

	if strings.TrimSpace(*tag) == "" {
		fmt.Fprintln(os.Stderr, "错误：--tag 必需，例如 --tag 0.6.377")
		flag.Usage()
		os.Exit(2)
	}

	// 本地模式：跳过五通道探测，只验磁盘上的三件套。规则与远端验收同源。
	if strings.TrimSpace(*localDir) != "" {
		runLocal(*tag, *arch, *localDir, *jsonOut)
		return
	}

	opts := release.Opts{
		Tag:      *tag,
		Arch:     *arch,
		Repos:    splitList(*repo),
		Timeout:  time.Duration(*timeout) * time.Second,
		Channels: splitList(*only),
		WorkDir:  *workdir,

		VerifyTimeout: time.Duration(*vtimeout) * time.Second,
		VerifyAll:     *vall,
	}

	// 每条通道各自设超时上限，避免一条卡死拖住整体。
	// 探测超时与下载超时分开：探测短（快速判定不可达），下载长（经镜像 5MB 约 60s）。
	// 验收阶段最坏要给 5 条通道各留一次完整下载超时（同一 URL 允许重试一次），
	// 否则全局 ctx 会提前取消、把「网络慢」误报成「下载失败」。
	budget := time.Duration(*timeout)*time.Second +
		time.Duration(*vtimeout)*time.Second*5 + 60*time.Second
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	start := time.Now()
	results := release.Probe(ctx, opts)
	if *verify {
		results = release.VerifyAll(ctx, opts, results)
	}
	total := time.Since(start)

	if *jsonOut {
		emitJSON(*tag, *arch, total, results, *verify)
	} else {
		emitText(*tag, *arch, total, results, *verbose, *verify)
	}
	os.Exit(exitCode(results, *verify, *strict))
}

// runLocal 执行本地验收并直接退出（0 通过 / 1 失败 / 2 参数错误）。
func runLocal(tag, arch, dir string, jsonOut bool) {
	art, err := release.VerifyLocal(tag, arch, dir)
	code := 0
	if err != nil {
		code = 1
	}

	if jsonOut {
		payload := map[string]any{
			"tag":       release.NormalizedTag(tag),
			"arch":      arch,
			"local_dir": dir,
			"verified":  err == nil,
			"artifact":  art,
			"error":     errString(err),
			"exit_code": code,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(payload)
		os.Exit(code)
	}

	if err != nil {
		fmt.Printf("❌ 本地验收失败：%s\n", err)
		os.Exit(code)
	}
	fmt.Printf("✅ 本地验收通过 %d sha=%s manifest=%s/%s elf=%s bundle=%s\n",
		art.Size, release.Short(art.SHA256), art.FPK.Version, art.FPK.Platform,
		art.FPK.ELFMachine, art.FPK.FrontendBundle)
	os.Exit(code)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// emitText 打印人类可读汇总。
func emitText(tag, arch string, total time.Duration, rs []release.Result, verbose, verify bool) {
	fmt.Printf("moo-release tag=%s arch=%s  用时=%s  公钥=%s\n",
		release.NormalizedTag(tag), arch, total.Round(time.Millisecond), release.PubkeyID())
	fmt.Println(strings.Repeat("-", 96))
	for _, r := range rs {
		status := "FAIL"
		if r.OK {
			status = "OK  "
		}
		fmt.Printf("[%s] %-8s %6dms", status, r.Channel, r.LatencyMS)
		switch {
		case !r.OK:
			fmt.Printf("  %s\n", r.Err)
		case !verify:
			fmt.Printf("  三件套齐全 %s\n", shorten(r.Assets.FPK))
		case r.Verified:
			a := r.Artifact
			fmt.Printf("  ✅ 校验通过 %.1fMB  sha=%s  manifest=%s/%s  elf=%s  bundle=%s\n",
				float64(a.Size)/1048576, release.Short(a.SHA256), a.FPK.Version,
				a.FPK.Platform, a.FPK.ELFMachine, a.FPK.FrontendBundle)
		case r.VerifySkipped:
			reason := r.VerifySkipReason
			if reason == "" {
				reason = "本轮未验收（不算失败）"
			}
			fmt.Printf("  ⏭  %s\n", reason)
		case r.VerifyTransport:
			fmt.Printf("  ⚠️  通道不可达，跳过验收：%s\n", r.VerifyErr)
		default:
			msg := r.VerifyErr
			if msg == "" {
				msg = "验收未通过但无错误详情（分类缺陷，请报 issue）"
			}
			fmt.Printf("  ❌ 契约违规：%s\n", msg)
		}
		if verbose {
			for _, e := range r.Extra {
				fmt.Printf("         · %s\n", e)
			}
		}
	}
	fmt.Println(strings.Repeat("-", 96))

	ok := release.Succeeded(rs)
	if len(ok) == 0 {
		fmt.Println("结论：五通道全部失败 —— 发布不可用")
		return
	}
	ver := 0
	for _, r := range rs {
		if r.Verified {
			ver++
		}
	}
	fmt.Printf("结论：%d/%d 通道可用（%s），%d 条通过客户端同款验收\n", len(ok), len(rs), strings.Join(ok, ", "), ver)
	if !contains(ok, release.ChannelAPI) {
		fmt.Println("注意：API 主通道不可用（国内常见 remaining=0），线上会走兜底通道；兜底已按客户端路径验收")
	}
}

func emitJSON(tag, arch string, total time.Duration, rs []release.Result, verify bool) {
	payload := map[string]any{
		"tag":         release.NormalizedTag(tag),
		"arch":        arch,
		"elapsed_ms":  total.Milliseconds(),
		"verify":      verify,
		"pubkey":      release.PubkeyID(),
		"channels":    rs,
		"ok_channels": release.Succeeded(rs),
		"exit_code":   exitCode(rs, verify, false),
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(payload)
}

// exitCode 语义：
//
//	0 —— 至少一条通道通过客户端同款验收，且没有「可达但校验失败」的通道
//	1 —— 全部不可达 / 有通道可达但三件套缺失或校验失败 / --strict 下列通道失败
//	2 —— 参数错误（在 main 里直接退出）
//
// api 通道因限流不可用属"不可达"，不单独判失败（国内常态，线上会走兜底）。
func exitCode(rs []release.Result, verify, strict bool) int {
	var ok, verified, hardFail int
	for _, r := range rs {
		if !r.OK {
			continue
		}
		ok++
		if !verify {
			continue
		}
		switch {
		case r.Verified:
			verified++
		case r.VerifyTransport, r.VerifySkipped:
			// 不可达/重复 URL 不算契约违规
		default:
			hardFail++
		}
	}
	if ok == 0 {
		return 1
	}
	if verify && (hardFail > 0 || verified == 0) {
		return 1
	}
	if strict && ok != len(rs) {
		return 1
	}
	return 0
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// shorten 只保留 URL 末两段，避免刷屏。
func shorten(u string) string {
	parts := strings.Split(strings.TrimSuffix(u, "/"), "/")
	if len(parts) <= 2 {
		return u
	}
	return ".../" + strings.Join(parts[len(parts)-2:], "/")
}
