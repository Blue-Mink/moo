package platform

import (
	"context"
	"errors"
	"time"
)

// InstallStaged 安装（或升级）已暂存包，并验证应用出现在已安装列表。
// params 为安装向导参数（调用方先用 AutoFillParams 自动填充；升级时忽略）。
// volume = 设置「应用安装位置」的用户配置卷（≤0 = 未设置 → DefaultVolume
// 探测/诚实报错；0.6.325 起透传，不再硬编码 0）。升级路径不受影响
// （沿用已装卷）。供「安装已下载缓存 FPK」等不经过应用源的路径使用。
func InstallStaged(ctx context.Context, staged *StagedPackage, params []WizardParam, volume int, onProgress func(float64)) error {
	if onProgress == nil {
		onProgress = func(float64) {}
	}
	if staged.Installed {
		if err := UpgradeFpk(ctx, staged, nil, onProgress); err != nil {
			return err
		}
	} else {
		if err := InstallFpk(ctx, staged, volume, params, onProgress); err != nil {
			return err
		}
	}
	return VerifyInstalled(ctx, staged.AppName)
}

// VerifyInstalled 等待应用出现在 daemon 已安装列表（安装回调有秒级延迟）。
func VerifyInstalled(ctx context.Context, appName string) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		list, err := ListInstalled(ctx)
		if err == nil {
			for _, a := range list {
				if a.AppName == appName {
					return nil
				}
			}
		}
		if time.Now().After(deadline) {
			return errors.New("安装回调已返回，但应用未出现在已安装列表（30s）")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
}
