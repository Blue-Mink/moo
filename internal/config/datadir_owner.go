package config

// 2026-11-26（P1-3）：数据目录归属标记。
//
// 背景：目录名启发式只能挡住「名字明显不是 moo」的目录；若别人家的目录恰好
// 也叫 moo，或同一台机器上 moo / moo-dev 两个变体共用一个目录，名字校验就
// 失效了。这里给数据目录写一个显式归属标记 <dir>/.moo-owner，内容是本二进制
// 的 appname（默认 moo，见 AppName）；读到不同值 = 这目录已被别人认领 → 拒绝。
//
// 0.6.574 收紧三处（对应复核 P1-a/b 与两条 P2）：
//  1. 只有「平台分配的目录」或「本次新建的目录」才盖章；借用别人已有目录只记
//     日志 —— 旧版在旁路下缺标记即盖章，一次调试就会把别人的目录永久认领；
//  2. 标记读不到 / 不是普通文件 → 拒绝（旧版静默放行，chmod 000 即可关掉守卫）；
//  3. 空标记在允许认领时补写；旧版遗留值 "moo" 在同名目录里迁移成 appname。
//
// 标记由启动时创建一次（0600，tmp+rename），创建失败只告警不阻断启动
// （写入侧仍会各自校验）。

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DataDirOwnerFile 数据目录归属标记文件名。
	DataDirOwnerFile = ".moo-owner"
	// defaultAppName 二进制自带的 appname（可用 -ldflags -X 注入 moo-dev 等变体）。
	defaultAppName = "moo"
	// legacyOwnerValue 0.6.573 及更早把标记值恒写成 "moo"（moo / moo-dev 不区分）。
	// 只在「目录名恰好等于本应用名」时认作自己的旧标记并允许迁移，见 ownerMatches。
	legacyOwnerValue = "moo"
)

// appName 二进制自身的 appname，构建期可注入（-X moo/internal/config.appName=...）。
var appName = defaultAppName

// AppName 本二进制所属应用的 appname。
//
// 0.6.574（P1-b）：标记值与目录名判定都必须区分 moo / moo-dev，原来硬编码
// "moo" 让两个变体互相认成自己人，守卫等于没有区分力。
//
// 刻意**不用** TRIM_APPNAME 做身份来源：从别的应用的 shell 里手动起本进程时
// 会继承对方的 TRIM_APPNAME（实测本机 shell 里就是 com.dustinky.qwenpaw），
// 那会把「别人的数据目录」误认成自己家 —— 正是这次事故的场景。
// 需要区分变体时用显式 MOO_APPNAME（或构建期 -X），二者都不会被无意继承。
func AppName() string {
	if v := strings.TrimSpace(os.Getenv("MOO_APPNAME")); v != "" {
		return v
	}
	return appName
}

// DataDirOwnerValue 标记文件应写入的值（= AppName()）。
func DataDirOwnerValue() string { return AppName() }

// normalizeOwner 归一标记内容：去 BOM / 首尾空白 / 统一小写。
// （P2：手工编辑过的 "MOO" / 带 BOM 的文件不该把用户锁在门外。）
func normalizeOwner(s string) string {
	s = strings.TrimPrefix(s, "\ufeff")
	return strings.ToLower(strings.TrimSpace(s))
}

// ownerMatches 标记值 v 是否属于本应用。dir 仅用于识别旧版遗留值：
// 旧版恒写 "moo"，若目录名恰是本应用名（appcenter 按 appname 建目录）就认作
// 自己的旧标记、允许后续迁移；否则一律视为别人占用。
func ownerMatches(dir, v string) bool {
	v = normalizeOwner(v)
	if v == "" {
		return true
	}
	app := normalizeOwner(AppName())
	if v == app {
		return true
	}
	return v == legacyOwnerValue && normalizeOwner(filepath.Base(filepath.Clean(dir))) == app
}

// isPlatformHome dir 是否是平台按 appname 分配给本应用的目录
// （TRIM_PKGVAR 指向它，且目录名 == AppName()）。只有这种目录和「本次新建的
// 空目录」才允许盖章。
func isPlatformHome(dir string) bool {
	pkgvar := strings.TrimSpace(os.Getenv("TRIM_PKGVAR"))
	if pkgvar == "" {
		return false
	}
	clean := filepath.Clean(pkgvar)
	return filepath.Clean(dir) == clean && filepath.Base(clean) == AppName()
}

// CheckDataDirOwner 校验 <dir>/.moo-owner：不存在 / 空 / 等于本应用 appname
// → nil；其它值 → 冲突错误。
//
// 0.6.574：读不到一律**拒绝**（旧版是放行）—— chmod 000、属主不对、被换成
// 目录/链接都能把守卫静默关掉；这里只对「确实不存在」放行，其余给可读原因，
// 并说明出路（修正权限或删除标记）。
func CheckDataDirOwner(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil
	}
	p := filepath.Join(dir, DataDirOwnerFile)
	fi, err := os.Lstat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("无法核对数据目录归属标记 %s: %w；拒绝写入（请修正该文件权限，或删除它后重启）", p, err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("数据目录归属标记 %s 不是普通文件（%s），拒绝写入（请删除它后重启）", p, fi.Mode().Type())
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return fmt.Errorf("无法读取数据目录归属标记 %s: %w；拒绝写入（请修正该文件权限，或删除它后重启）", p, err)
	}
	v := normalizeOwner(string(b))
	if !ownerMatches(dir, v) {
		return fmt.Errorf("数据目录 %q 已被 %q 占用（标记 %s），拒绝写入；如确需使用请换目录、删除该标记或显式设置 MOO_DATA（%s）", dir, v, DataDirOwnerFile, p)
	}
	return nil
}

// ReadDataDirOwner 读取 <dir>/.moo-owner 的原始值（空 = 无标记）。
// 只给自监控/诊断端点用；判定逻辑仍走 CheckDataDirOwner。
func ReadDataDirOwner(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", nil
	}
	b, err := os.ReadFile(filepath.Join(dir, DataDirOwnerFile))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return normalizeOwner(string(b)), nil
}

// EnsureDataDirOwner 启动时确保归属标记存在（幂等）：先过 CheckDataDir
// （含归属冲突校验），缺失则创建 0600 标记（tmp+rename）。
//
// 0.6.574（P1-a）：只有「平台分配的目录」（TRIM_PKGVAR == dir 且目录名 ==
// AppName()）或「本次新建的目录」才盖章。旧版在旁路（显式 MOO_DATA /
// MOO_ALLOW_FOREIGN_DATA=1）下缺标记即盖章 —— 一次调试运行就会把别人的目录
// 永久认领，之后任何读该标记的应用都会拒绝启动，且守卫在这条路径上永久失效。
// 借用别人已有目录时只记一行日志，不动文件。
func EnsureDataDirOwner(dir string) error {
	if err := CheckDataDir(dir); err != nil {
		return err
	}
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil
	}
	p := filepath.Join(dir, DataDirOwnerFile)

	_, statErr := os.Stat(dir)
	created := os.IsNotExist(statErr)
	if created {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	claim := created || isPlatformHome(dir)

	// 旧版遗留值（恒 "moo"）在「允许认领 + 目录名是本应用名」时迁移成本应用
	// 的 appname —— 否则 moo 会一直把 moo-dev 的目录认成自己家。
	migrateLegacy := false
	if b, err := os.ReadFile(p); err == nil {
		v := normalizeOwner(string(b))
		migrateLegacy = claim && v == legacyOwnerValue && v != normalizeOwner(AppName())
		switch {
		case v != "" && !migrateLegacy:
			return nil // 已有有效标记（冲突已在 CheckDataDir 拒绝）
		case v == "" && !claim:
			log.Printf("[datadir] 目录 %s 的归属标记为空，借用模式不补写", dir)
			return nil
		}
	}
	if !claim {
		log.Printf("[datadir] 目录 %s 无归属标记（TRIM_PKGVAR=%q 未匹配平台目录，AppName=%s），按显式 MOO_DATA/放行开关使用（不盖章）", dir, os.Getenv("TRIM_PKGVAR"), AppName())
		return nil
	}
	if migrateLegacy {
		log.Printf("[datadir] 目录 %s 的旧标记 moo 迁移为 %s", dir, AppName())
	}

	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(DataDirOwnerValue()+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Chmod(p, 0o600)
}
