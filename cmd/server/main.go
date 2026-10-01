// Command moo-server 是 Moo 应用商店守护进程。
//
// 访问模型（2026-09-21 定案）：
//   - 主入口：飞牛统一网关 —— unix socket（MOO_SOCKET / ${TRIM_APPDEST}/app.sock），
//     请求携带 GatewayPrefix(/app/moo) 前缀与 X-Trim-* 身份头（可信）
//   - 调试通道：127.0.0.1 TCP（MOO_WEB_PORT，默认 38100），不出 LAN，不信任身份头
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"moo/internal/api"
	"moo/internal/config"
	"moo/internal/netx"
	"moo/internal/notify"
	"moo/internal/official"
	"moo/internal/operation"
	"moo/internal/pipeline"
	"moo/internal/secret"
	"moo/internal/source"
	"moo/internal/task"
	webdist "moo/web"
)

// Version 由 -ldflags 注入，默认 dev。
var Version = "dev"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	dataDir := config.DataDir()
	// 0.6.220（方案 X）：敏感字段落盘加密——注入加解密器后再 Load，
	// 这样读到的面板口令/通知渠道密钥已在内存里是明文。
	// 密钥 = 每次安装独有的随机值（<dataDir>/.moo-secret.key，0600）；
	// 不用 /etc/machine-id 派生（实测多台 fnOS 设备 machine-id 相同）。
	if sealer, serr := secret.LoadOrCreate(dataDir); serr != nil {
		log.Printf("[security] 凭据加密密钥不可用，凭据将以明文存储（建议检查数据目录权限）: %v", serr)
	} else {
		config.SetCodec(sealer)
		config.SetExtraCodec(notify.SealChannelSecrets)
		log.Printf("[security] 凭据落盘加密已启用（密钥指纹 %s）", sealer.KeyID())
	}
	cfg, err := config.Load(dataDir)
	if err != nil {
		log.Printf("配置加载失败，使用默认配置: %v", err)
	}
	// 存量明文凭据 → 立刻改写为密文（一次性迁移，幂等）
	if config.MigrationNeeded() {
		if err := cfg.Save(dataDir); err != nil {
			log.Printf("[security] 明文凭据迁移失败（下次保存会重试）: %v", err)
		} else {
			log.Printf("[security] 已把存量明文凭据迁移为密文（enc:v1）")
		}
	}
	if cfg.SecretDecryptFailed {
		log.Printf("[security] 存在无法解密的凭据（密钥文件缺失/变更）——相关功能需在设置页重新填写")
	}
	// 0.6.207-panel：启动时清理旧版本遗留的明文凭据残留（config.bak-*/
	// *.bak-removed/official_session.json*/含明文口令的备份快照），逐条留痕。
	if removed := api.CleanResidualCredentials(dataDir); len(removed) > 0 {
		log.Printf("[security] 启动清理历史凭据残留 %d 个: %v", len(removed), removed)
	}
	if p := os.Getenv("MOO_WEB_PORT"); p != "" {
		cfg.WebPort = p
	}
	// 科学加速（0.6.206）：启动时恢复已保存的本机代理（GitHub 域名走代理）。
	if cfg.ProxyEnabled && cfg.ProxyURL != "" {
		netx.SetProxy(cfg.ProxyURL)
		log.Printf("[accel] 科学加速已启用: %s", cfg.ProxyURL)
	}

	srcMgr := source.NewManager(cfg)
	// 目录落盘快照：启动秒开（对标 FnDepot 的 SQLite 目录持久化）
	srcMgr.SetCachePath(dataDir + "/cache/catalog.json")
	if n := srcMgr.LoadCache(dataDir + "/cache/catalog.json"); n > 0 {
		log.Printf("目录快照已恢复: %d 个源（后台自动刷新后更新）", n)
	}
	// 下载目录：历史配置是相对名（dataDir/downloads）；设置页选择器落地后
	// 为绝对路径（/volN/共享目录）。绝对路径直接使用。
	dlDir := cfg.DownloadDir
	if !filepath.IsAbs(dlDir) {
		dlDir = dataDir + "/" + dlDir
	}
	eng := task.NewEngine(task.DetectAria2(cfg.Aria2Bin))
	if eng.Aria2Bin != "" {
		log.Printf("aria2 可用: %s（≥2MB 下载启用多连接）", eng.Aria2Bin)
	} else {
		log.Printf("aria2 不可用，下载走内置单连接")
	}
	tasks, err := task.NewManager(dlDir, eng)
	if err != nil {
		log.Fatalf("下载目录初始化失败: %v", err)
	}
	ops := operation.NewQueue()
	pipe := pipeline.New(srcMgr, dataDir+"/staging", dlDir)
	pipe.Engine = eng

	srv := &api.Server{
		Version:   Version,
		StartedAt: time.Now(),
		Cfg:       cfg,
		Src:       srcMgr,
		Tasks:     tasks,
		Ops:       ops,
		Pipe:      pipe,
		WebFS:     webdist.Dist,
		Mirrors:   api.NewMirrorMonitor(),
		Panel:     api.NewPanel(cfg),
		Official:  official.NewManager(dataDir),
	}
	pipe.FPKCandidates = srv.FpkCandidates

	// 官方应用中心：加载持久化 OAuth 会话（token 未过期则免重新授权）
	srv.Official.LoadSession()

	// 出站抓取安全策略（2026-09-27 代码审核）：源数据驱动的 URL
	// （readme/preview/icon/download）只放行公共地址，防 SSRF。
	srv.SetupNetguard()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 源自动刷新：启动 10s 后全量并发刷一次 + 按 check_interval_hours 周期刷
	// （此前源目录只在用户手动操作时更新，重启后社区目录为空）
	go srv.StartSourceAutoRefresh(ctx)

	// 加速源后台测速：启动 10s 后先探一次 + 每 5 分钟自动重探
	// （智能选路/下载/图标竞速始终吃最新测速数据，无需打开设置面板）
	go srv.StartMirrorProbeLoop(ctx)

	// 设置自动备份（config 快照）+ FPK 下载缓存自动清理：每 10 分钟检查一次
	go srv.StartBackupLoop(ctx)

	// 0.6.216 P2（N3）：moo.log 大小轮转（>5MB → 归档 .1/.2 + 截断）
	go srv.StartLogRotator(ctx)

	// 自监控：每 5 分钟自检资源占用（内存/CPU 阈值告警）+ 后端健康
	//（appcenter daemon RPC 可达 + config 可读），异常/恢复推送（0.6.121）
	go srv.StartSelfMonitor(ctx)

	// 欢迎语（0.6.155）：安装后首次启动自动推送「欢迎使用Moo」一次，
	// 重启/升级不重复（WelcomeSentAt 持久化）；手动重发走 POST /api/notify/welcome。
	go srv.MaybeFireWelcome(ctx)

	// GitHub Release 资产下载量同步（第三方源目录不统计下载量的补数通道）
	srv.GHStats = source.NewGHStats(dataDir + "/cache/ghstats.json")
	go srv.StartGHStatsRefresh(ctx)

	// 图标预热：目录就绪后 6 并发拉取缺失图标进两级缓存（内存 10min +
	// 磁盘 7 天）——对齐 New Store 的加载架构，消除「图标冷瀑布」
	go srv.StartIconWarm(ctx)

	// ---- 主入口：unix socket（统一网关） ----
	var unixSrv *http.Server // 网关入口服务器（下方赋值，退出时一并优雅关闭）
	socketPath := config.SocketPath()
	_ = os.Remove(socketPath) // 清掉上次残留
	unixLn, err := net.Listen("unix", socketPath)
	if err != nil {
		log.Printf("unix socket 监听失败（网关入口不可用，继续 TCP）: %v", err)
	} else {
		// 安全（2026-09-27 审核）：网关通道信任对端注入的 X-Trim-* 头，
		// socket 权限收紧到属主（root）——即使上级目录权限将来放宽，
		// 非 root 进程也无法连接伪造管理员身份。
		_ = os.Chmod(socketPath, 0o700)
		log.Printf("unix socket 监听: %s", socketPath)
		// 超时策略（2026-10-01 API 审计）：只设读头与空闲超时；WriteTimeout 故意不设——
		// 安装/更新/向导走 SSE 长流（向导单次最长 12 分钟），设了会中途掐断流式响应。
		unixSrv = &http.Server{
			Handler:           srv.Handler(true),
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
		}
		go func() {
			if err := unixSrv.Serve(unixLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("unix socket 服务异常: %v", err)
			}
		}()
		defer func() {
			unixLn.Close()
			_ = os.Remove(socketPath)
		}()
	}

	// ---- 调试通道：127.0.0.1 TCP（不出 LAN） ----
	tcpAddr := "127.0.0.1:" + cfg.WebPort
	tcpLn, err := net.Listen("tcp", tcpAddr)
	if err != nil {
		log.Fatalf("TCP 监听失败: %v", err)
	}
	log.Printf("调试通道监听: http://%s", tcpAddr)

	httpSrv := &http.Server{
		Handler:           srv.Handler(false),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// WriteTimeout 故意不设：安装/更新为 SSE 长流（同网关入口）。
	}
	go func() {
		<-ctx.Done()
		srv.StopIcons()       // 图标两级缓存收尾落盘
		srv.StopReadmeStore() // README 两级缓存收尾落盘
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if unixSrv != nil {
			_ = unixSrv.Shutdown(shutCtx)
		}
		_ = httpSrv.Shutdown(shutCtx)
	}()
	if err := httpSrv.Serve(tcpLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("TCP 服务异常: %v", err)
	}
}
