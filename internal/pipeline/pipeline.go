// Package pipeline 编排 Moo 的安装/升级/卸载操作流：
// 源应用 → 本地 FPK（复用下载或直下）→ daemon 暂存 → 向导自动填充 → 安装任务 → 验证。
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"moo/internal/platform"
	"moo/internal/source"
	"moo/internal/task"
)

// ProgressFn 是进度回调（消息 + 0-100）。
type ProgressFn func(msg string, pct float64)

// Pipeline 聚合管道依赖。
type Pipeline struct {
	Src       *source.Manager
	Staging   string // 临时 FPK 目录（{dataDir}/staging）
	Downloads string // 已下载 FPK 目录（{dataDir}/downloads）
	Engine    *task.Engine // 统一下载引擎（aria2 多连接 + 镜像候选 + 续传）
	// FPKCandidates 生成 GitHub FPK 候选 URL 顺序（可选；nil = 仅直连）
	FPKCandidates func(app *source.App) []string
}

func New(src *source.Manager, staging, downloads string) *Pipeline {
	_ = os.MkdirAll(staging, 0o755)
	_ = os.MkdirAll(downloads, 0o755)
	return &Pipeline{Src: src, Staging: staging, Downloads: downloads}
}

// SetDownloads 运行时切换已下载 FPK 目录（设置页「FPK 下载目录」选择器用）。
// 调用方负责先迁移旧目录缓存文件。
func (p *Pipeline) SetDownloads(dir string) {
	_ = os.MkdirAll(dir, 0o755)
	p.Downloads = dir
}

// Install 从源安装（或升级）应用，向导参数自动填充，全流程阻塞至完成。
func (p *Pipeline) Install(ctx context.Context, sourceName, appName string, progress ProgressFn) error {
	return p.InstallWithParams(ctx, sourceName, appName, nil, progress)
}

// InstallWithParams 安装/升级；params 非空时用作向导参数（跳过自动填充）。
func (p *Pipeline) InstallWithParams(ctx context.Context, sourceName, appName string, params []platform.WizardParam, progress ProgressFn) error {
	if progress == nil {
		progress = func(string, float64) {}
	}
	app := p.Src.Get(sourceName, appName)
	if app == nil {
		return fmt.Errorf("应用不存在: %s/%s", sourceName, appName)
	}
	if app.DownloadURL == "" {
		return fmt.Errorf("该应用没有下载链接")
	}

	// 1) 本地 FPK
	progress("准备安装包…", 2)
	fpkPath, err := p.ensureFpk(ctx, app, progress)
	if err != nil {
		return err
	}

	// 2) 暂存
	progress("暂存安装包…", 8)
	staged, err := platform.StageFpk(ctx, fpkPath, func(f float64) {
		progress("暂存安装包…", 8+f*0.12)
	})
	if err != nil {
		return err
	}
	// 注意：暂存目录不能在 defer 里清理——客户端断开/任务被平台回收时
	// 平台侧安装任务（及其 ~3 分钟自动重试）仍在运行，删掉 -tpk 会导致
	// 平台报 10111 manifest not exist。只在最终成功后清理（见函数尾部）。

	what := "安装"
	if staged.Installed {
		what = "升级"
	}

	// 2.5) 包版本交叉校验（防「源声明版本 ≠ 实际包」的假升级）：
	// 升级时包版本必须严格高于已装版本，否则平台任务会「成功」地把
	// 旧版本再装一遍，版本纹丝不动却从「有更新」里消失不了。
	if staged.Installed {
		if inst, err := platform.FindInstalled(ctx, staged.AppName); err == nil && inst != nil &&
			!source.IsNewer(staged.Version, inst.Version) {
			return fmt.Errorf("源提供的包版本为 %s，并不比已安装的 %s 更新——源的版本数据可能有误，本次未执行更新", staged.Version, inst.Version)
		}
	}
	if !source.VersionEqual(staged.Version, app.Version) && source.VersionLess(staged.Version, app.Version) {
		progress(fmt.Sprintf("包实际版本为 %s（源声明 %s），按实际版本继续…", staged.Version, app.Version), 20)
	}

	// 3) 向导参数：前端传入则直接用；否则自动填充（取 initValue；必填无默认 → 明确报错）
	progress("读取安装向导…", 20)
	if params == nil {
		var missing []string
		params, missing, err = platform.AutoFillParams(ctx, staged)
		if err != nil {
			return err
		}
		if len(missing) > 0 {
			return fmt.Errorf("以下字段必填且无默认值: %s（请先在飞牛官方应用中心安装该应用并配置）", strings.Join(missing, "、"))
		}
	}

	// 4) 安装/升级
	if staged.Installed {
		progress(fmt.Sprintf("升级 %s → %s…", app.Name, staged.Version), 30)
		err = platform.UpgradeFpk(ctx, staged, params, func(f float64) {
			progress("升级中…", 30+f*0.65)
		})
	} else {
		progress(fmt.Sprintf("安装 %s %s…", app.DisplayName, app.Version), 30)
		err = platform.InstallFpk(ctx, staged, 0, params, func(f float64) {
			progress("安装中…", 30+f*0.65)
		})
	}
	if err != nil {
		return err
	}

	// 5) 验证结果：新装=出现在已装列表；升级=已装版本必须达到目标版本
	// （平台「升级任务成功」也可能只是把旧版本再装一遍，版本不升反是
	// 假成功——必须以版本为准回查）
	progress("验证安装结果…", 97)
	if err := p.verifyResult(ctx, staged.AppName, staged.Version, staged.Installed); err != nil {
		return err
	}
	// 安装确定成功（任务已终态）才清理暂存目录——daemon 不自动回收，
	// 不清理会累积；失败/未知路径保留，供平台自动重试复用。
	platform.RemoveStagedPackage(staged.Path)
	progress(fmt.Sprintf("%s完成", what), 100)
	return nil
}

// StageOnly 下载（或复用）FPK 并暂存，返回暂存包（供读取向导定义，不安装）。
func (p *Pipeline) StageOnly(ctx context.Context, sourceName, appName string, progress ProgressFn) (*platform.StagedPackage, error) {
	if progress == nil {
		progress = func(string, float64) {}
	}
	app := p.Src.Get(sourceName, appName)
	if app == nil {
		return nil, fmt.Errorf("应用不存在: %s/%s", sourceName, appName)
	}
	if app.DownloadURL == "" {
		return nil, fmt.Errorf("该应用没有下载链接")
	}
	fpkPath, err := p.ensureFpk(ctx, app, progress)
	if err != nil {
		return nil, err
	}
	return platform.StageFpk(ctx, fpkPath, func(f float64) {
		progress("暂存安装包…", f)
	})
}

// ensureFpk 返回本地 FPK 路径：downloads/staging 里已有完整文件则复用，
// 否则走统一下载引擎（aria2 多连接 + 镜像候选）下载到持久缓存。
func (p *Pipeline) ensureFpk(ctx context.Context, app *source.App, progress ProgressFn) (string, error) {
	fileName := filepath.Base(app.Name) + ".fpk"
	sizeCands := task.ParseSizeMB(app.SizeMB)
	sizeBytes := app.SizeBytes
	if sizeBytes <= 0 && len(sizeCands) > 0 {
		sizeBytes = sizeCands[0]
	}
	// 复用已下载（A3：严格核验——缓存文件名跨源共享 <appname>.fpk，
	// 其他源/其他版本的同名文件不得误复用；排除 aria2 续传残留）
	for _, dir := range []string{p.Downloads, p.Staging} {
		cand := filepath.Join(dir, fileName)
		info, err := os.Stat(cand)
		if err != nil || info.Size() <= 0 {
			continue
		}
		if _, err := os.Stat(cand + ".aria2"); err == nil {
			continue // aria2 续传残留 = 未完成
		}
		if reuseOK(cand, app, info.Size(), sizeCands) {
			progress("复用已下载的安装包", 5)
			return cand, nil
		}
	}
	// 下载落持久缓存（与「下载 FPK」任务共享，装完不丢）
	dst := filepath.Join(p.Downloads, fileName)
	progress("下载安装包…", 5)
	var cands []string
	if p.FPKCandidates != nil {
		cands = p.FPKCandidates(app)
	}
	if len(cands) == 0 {
		cands = []string{app.DownloadURL}
	}
	var lastPct float64
	err := p.Engine.Download(ctx, task.DownloadOpts{
		DestPath:   dst,
		Candidates: cands,
		SizeBytes:  sizeBytes,
		Progress: func(done, size int64) {
			if size <= 0 {
				return
			}
			pct := 5 + float64(done)/float64(size)*0.3
			if pct-lastPct > 0.5 || pct >= 35 {
				progress(fmt.Sprintf("下载安装包… %.1fMB/%.1fMB", float64(done)/(1<<20), float64(size)/(1<<20)), pct)
				lastPct = pct
			}
		},
	})
	if err != nil {
		return "", fmt.Errorf("下载安装包失败: %w", err)
	}
	// 下载后核验源声明的 sha256/精确大小：源存在「版本声明领先于包文件」
	// 的滚动覆盖模式（多版本指向同一仓库文件），直接照装会把旧包当新版
	// 升级，表现为「升级成功但版本不变」的假成功。
	if err := verifyDownloaded(dst, app); err != nil {
		return "", err
	}
	return dst, nil
}

// reuseOK 判断现有缓存文件能否作为当前源当前版本的包复用（三级核验，
// 有强证据用强证据，全无时用 manifest 版本兜底）：
//  1. 源提供 sha256 → 文件哈希必须一致（最强）
//  2. 源提供精确字节大小 → 必须一致（否则回退粗粒度 MB 容差）
//  3. 两者皆无 → 解析 manifest 的 version 与声明版本一致才复用
func reuseOK(cand string, app *source.App, fileSize int64, sizeCands []int64) bool {
	if app.Sha256 != "" {
		actual, err := Sha256OfFile(cand)
		return err == nil && strings.EqualFold(actual, app.Sha256)
	}
	if app.SizeBytes > 0 {
		if fileSize != app.SizeBytes {
			return false
		}
	} else if len(sizeCands) > 0 && !task.SizeMatches(fileSize, sizeCands...) {
		return false
	}
	if app.Version != "" {
		v, err := ManifestVersion(cand)
		if err != nil || !source.VersionEqual(v, app.Version) {
			return false
		}
	}
	return true
}

// verifyDownloaded 下载完成后按源声明核验包体（sha256 优先，其次精确大小）。
// 不匹配时给出「实际版本 vs 声明版本」的明确错误（不再静默照装）。
func verifyDownloaded(path string, app *source.App) error {
	if app.Sha256 != "" {
		actual, err := Sha256OfFile(path)
		if err != nil {
			return fmt.Errorf("校验安装包失败: %w", err)
		}
		if strings.EqualFold(actual, app.Sha256) {
			return nil
		}
		return sourceMismatchErr(app, path,
			fmt.Sprintf("源声明的版本 %s 与提供的包内容不符（校验和不匹配）", app.Version))
	}
	if app.SizeBytes > 0 {
		if fi, err := os.Stat(path); err == nil && fi.Size() != app.SizeBytes {
			return sourceMismatchErr(app, path,
				fmt.Sprintf("源声明的版本 %s 与提供的包大小不符（期望 %d 字节，实际 %d 字节）", app.Version, app.SizeBytes, fi.Size()))
		}
	}
	return nil
}

// sourceMismatchErr 生成「源声明版本与实际包不符」的明确错误。
func sourceMismatchErr(app *source.App, path, head string) error {
	msg := head
	if v, err := ManifestVersion(path); err == nil && v != "" && !source.VersionEqual(v, app.Version) {
		msg += fmt.Sprintf("，包内实际版本为 %s", v)
	}
	msg += "——源的包文件可能未及时更新，请等源维护者修复后再试，或从其他源安装"
	return errors.New(msg)
}

// verifyResult 验证安装/升级的最终状态（60s 上限，2s 轮询）：
//   - 新装：应用出现在已安装列表即通过
//   - 升级：已装版本必须达到 targetVersion（平台可能报告任务成功但
//     实际装回的仍是旧版本，仅查存在性会放行假成功）
func (p *Pipeline) verifyResult(ctx context.Context, appName, targetVersion string, isUpgrade bool) error {
	deadline := time.Now().Add(60 * time.Second)
	lastSeen := ""
	var lastListErr error
	for {
		list, err := platform.ListInstalled(ctx)
		lastListErr = err
		if err == nil {
			for _, a := range list {
				if a.AppName != appName {
					continue
				}
				lastSeen = a.Version
				if !isUpgrade {
					return nil
				}
				if source.VersionEqual(a.Version, targetVersion) || source.IsNewer(a.Version, targetVersion) {
					return nil
				}
			}
		}
		if time.Now().After(deadline) {
			if isUpgrade {
				if lastSeen != "" {
					return fmt.Errorf("平台报告升级完成，但实际安装版本仍为 %s（期望 %s）——源提供的包可能不是新版本", lastSeen, targetVersion)
				}
				if lastListErr != nil {
					return fmt.Errorf("验证升级结果失败: %w", lastListErr)
				}
				return fmt.Errorf("验证升级结果失败: 应用未出现在已安装列表")
			}
			if lastListErr != nil {
				return fmt.Errorf("验证安装结果失败: %w", lastListErr)
			}
			return fmt.Errorf("应用未出现在已安装列表")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// Uninstall 卸载应用（默认保留 @appdata）。
func (p *Pipeline) Uninstall(ctx context.Context, appname string, deleteData bool, progress ProgressFn) error {
	if progress == nil {
		progress = func(string, float64) {}
	}
	what := "卸载"
	if deleteData {
		what = "卸载并删除数据"
	}
	progress(fmt.Sprintf("%s %s…", what, appname), 5)
	return platform.Uninstall(ctx, appname, deleteData, func(f float64) {
		progress("卸载中…", 5+f*0.9)
	})
}

// StartApp 启动已安装应用。
func (p *Pipeline) StartApp(ctx context.Context, appname string, progress ProgressFn) error {
	if progress == nil {
		progress = func(string, float64) {}
	}
	progress(fmt.Sprintf("启动 %s…", appname), 10)
	return platform.StartApp(ctx, appname)
}

// StopApp 停用已安装应用。
func (p *Pipeline) StopApp(ctx context.Context, appname string, progress ProgressFn) error {
	if progress == nil {
		progress = func(string, float64) {}
	}
	progress(fmt.Sprintf("停用 %s…", appname), 10)
	return platform.StopApp(ctx, appname)
}
