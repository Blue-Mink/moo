// Command moo-server 是 Moo 应用商店守护进程。
//
// 访问模型（2026-09-21 定案）：
//   - 主入口：飞牛统一网关 —— unix socket（MOO_SOCKET / ${TRIM_APPDEST}/app.sock），
//     请求携带 GatewayPrefix(/app/moo) 前缀与 X-Trim-* 身份头（可信）
//   - 调试通道：127.0.0.1 TCP（MOO_WEB_PORT，默认 38100），不出 LAN，不信任身份头
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof" // 0.6.312 B1/F3：性能端点（注册到默认 mux，见下方监听）
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
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

	// 0.6.312 B1/F2：GOMAXPROCS 限定 4——Moo 的并发由信号量限死（源刷新 8、
	// 图标预热 3、操作队列 1），16 核机器上多出的 worker 只是 parked 线程
	// + 更多 GC 扫描对象（74 线程 / 454MB 峰值的主因之一）。env 可覆盖。
	if n := os.Getenv("MOO_GOMAXPROCS"); n != "" {
		if v, perr := strconv.Atoi(n); perr == nil && v > 0 {
			runtime.GOMAXPROCS(v)
		}
	} else {
		if prev := runtime.GOMAXPROCS(4); prev != 4 {
			log.Printf("[perf] GOMAXPROCS %d → 4（并发由信号量限死，多核无收益）", prev)
		}
	}

	// 0.6.313 B4/F10：Go 堆软限额 + GOGC 逃生阀。
	// 走 debug.SetMemoryLimit（语义=GOMEMLIMIT），**不用** runtime 原生 GOMEMLIMIT
	// env——双来源（代码 MOO_GOMEMLIMIT + env GOMEMLIMIT）部署时易混乱，只走代码
	// 调用（调用优先），FPK 的 start_daemon 也不注入 GOMEMLIMIT。
	// 0.6.315 分机校准：默认 256MB → 128MB。依据 .2 实测（2118 应用，B4 三件套
	// 后）：空闲稳态 RSS 85.9MB / 活堆 15.5MB / 99 条 E2E 风暴峰 236MB（瞬态）；
	// 128MB 软限下稳态保留堆进一步收紧（GC 更积极归页），瞬态峰由 GC 追赶消化。
	// 应用规模 5000+ 的机器 env 调 256MB/512MB（限额是天花板不是配额）；
	// 0/负数=不设（回到 0.6.312 行为，逃生阀）。软限语义：持续高压分配下 GC
	// 追不上会超发（非硬 kill；不改善冷启动 VmPeak 突发，只削保留堆的尾部）。
	memLimit := int64(128 << 20)
	if v := os.Getenv("MOO_GOMEMLIMIT"); v != "" {
		if n, perr := parseMemLimit(v); perr != nil {
			log.Printf("[perf] MOO_GOMEMLIMIT=%q 非法（支持裸字节或 数字+KB/MB/GB，如 128MB），用默认 128MB", v)
		} else if n == 0 {
			memLimit = 0
		} else {
			memLimit = n
		}
	}
	if memLimit > 0 {
		debug.SetMemoryLimit(memLimit)
	}
	// MOO_GOGC：仅显式设置且为正整数时应用（逃生阀；平时不动 GC 频率）。
	gogcNote := "未改（runtime 默认）"
	if g := os.Getenv("MOO_GOGC"); g != "" {
		if n, perr := strconv.Atoi(g); perr == nil && n > 0 {
			debug.SetGCPercent(n)
			gogcNote = strconv.Itoa(n)
		} else {
			log.Printf("[perf] MOO_GOGC=%q 非法（正整数），不应用", g)
		}
	}
	if memLimit > 0 {
		log.Printf("[perf] F10 内存限额生效: 堆软限 %d 字节（%.0fMB），GOGC=%s", memLimit, float64(memLimit)/(1<<20), gogcNote)
	} else {
		log.Printf("[perf] F10 内存限额生效: 堆软限未设置（MOO_GOMEMLIMIT=0），GOGC=%s", gogcNote)
	}

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
	// 0.6.247：首装自动填充内置默认源全集（0.6.316 起 20 源，基准=Blue-Mink
	// 精选清单；此前 0.6.314 起 157 源=验收过的备用测试机全量源+fn-knock
	// 官方源，已废弃）。全新安装即带全部默认源，离线可用；「恢复默认源」
	// 按钮以同一集合为基准（误删可一键找回）。
	if _, serr := os.Stat(config.Path(dataDir)); os.IsNotExist(serr) {
		if urls := source.BundledDefaultSources(); len(urls) > 0 {
			var seeded []config.SourceRef
			// 0.6.248：按名去重——基准集含 7 组同 owner 双仓库（owner 命名
			// 会重名），旧逻辑生成重名 SourceRef，落盘后按名折叠丢 7 条。
			// 冲突方改用 UniqueSourceName（owner-repo 归一名，如
			// tzi-shue-fndepot），清单内全部唯一入库（0.6.316 起 20 条）。
			taken := make(map[string]bool, len(urls))
			for _, u := range urls {
				name := source.UniqueSourceName(u, taken)
				if name == "" {
					continue
				}
				taken[name] = true
				seeded = append(seeded, config.SourceRef{Name: name, URL: u})
			}
			if len(seeded) > 0 {
				cfg.Sources = seeded
				if err := cfg.Save(dataDir); err != nil {
					log.Printf("[sources] 首装填充 %d 个内置默认源，落盘失败（下次保存重试）: %v", len(seeded), err)
				} else {
					log.Printf("[sources] 首装：已填充 %d 个内置默认源", len(seeded))
				}
			}
		}
		// 0.6.249：应用安装向导（install_callback 落盘的 wizard-install.json）
		// 里选择的下载目录——仅首装生效（存量配置不覆盖用户既有设置）。
		if wi, werr := os.ReadFile(filepath.Join(dataDir, "wizard-install.json")); werr == nil {
			var wz struct {
				DownloadDir string `json:"download_dir"`
			}
			if json.Unmarshal(wi, &wz) == nil && wz.DownloadDir != "" {
				if wz.DownloadDir == "downloads" {
					// 默认值，与内置一致，无需动作
				} else if filepath.IsAbs(wz.DownloadDir) {
					cfg.DownloadDir = wz.DownloadDir
					log.Printf("[wizard] 首装：下载目录按向导选择 = %s", wz.DownloadDir)
				} else if !strings.Contains(wz.DownloadDir, "..") {
					cfg.DownloadDir = wz.DownloadDir
					log.Printf("[wizard] 首装：下载目录按向导选择 = %s（相对数据目录）", wz.DownloadDir)
				}
			}
		}
	}
	// 0.6.246：存量数据自愈——0.6.141 之前入库的无协议源地址统一补
	// https://（写入路径修复不覆盖存量；不修则应用源列表首源显示/
	// 复制出来是不带 http 的地址，如 github.com/Blue-Mink/FnDepot）。
	if n := source.HealLegacyURLs(cfg); n > 0 {
		if err := cfg.Save(dataDir); err != nil {
			log.Printf("[sources] 无协议源地址自愈 %d 个，落盘失败（下次保存重试）: %v", n, err)
		} else {
			log.Printf("[sources] 存量无协议源地址已自愈: %d 个补 https://", n)
		}
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

	// 0.6.253：OAuth 免登录通道接线（有效会话优先，失败回退面板通道）
	api.WireOfficialOAuth(srv)

	// 0.6.312 B3/F8（源缓存两级）：源同步时把 changelog 全文/releases 明细
	// 移入详情磁盘层、从常驻内存剥离（详情打开时懒载，readme_store 同款）
	srcMgr.OnFetched = srv.DetailStoreSink

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

	// 0.6.312 B1/F3：pprof 性能端点——只绑 127.0.0.1（不出本机，零 LAN
	// 暴露），供内存/CPU 热点分析（此前最大盲区=无量具）。MOO_PPROF_PORT
	// 可改端口，=0 关闭。
	go func() {
		port := "38101"
		if v := os.Getenv("MOO_PPROF_PORT"); v != "" && v != "0" {
			port = v
		} else if os.Getenv("MOO_PPROF_PORT") == "0" {
			return // 显式关闭
		}
		ln, lerr := net.Listen("tcp", "127.0.0.1:"+port)
		if lerr != nil {
			log.Printf("[pprof] 监听 127.0.0.1:%s 失败: %v", port, lerr)
			return
		}
		log.Printf("[pprof] 性能端点就绪（仅本机）: http://127.0.0.1:%s/debug/pprof/", port)
		_ = http.Serve(ln, nil) // 默认 mux：net/http/pprof 的 index/heap/...
	}()

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
		srv.StopIcons()        // 图标两级缓存收尾落盘
		srv.StopReadmeStore()  // README 两级缓存收尾落盘
		srv.StopDetailStore() // 0.6.312 B3/F8：详情两级缓存收尾落盘
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

// parseMemLimit 解析 MOO_GOMEMLIMIT：裸字节数（"268435456"）或 数字+单位
// （KB/MB/GB/B，大小写不敏感，如 "256MB"/"512mb"）。
// 空串 / "0" / 负数 → (0, nil) = 不设限额；无法解析 → error（调用方落默认值）。
func parseMemLimit(v string) (int64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, nil
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n < 0 {
			return 0, nil
		}
		return n, nil
	}
	upper := strings.ToUpper(v)
	for _, u := range []struct {
		suf  string
		mult int64
	}{
		{"GB", 1 << 30},
		{"MB", 1 << 20},
		{"KB", 1 << 10},
		{"B", 1},
	} {
		if strings.HasSuffix(upper, u.suf) {
			n, err := strconv.ParseInt(strings.TrimSpace(upper[:len(upper)-len(u.suf)]), 10, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid memory limit %q", v)
			}
			if n < 0 {
				return 0, nil
			}
			return n * u.mult, nil
		}
	}
	return 0, fmt.Errorf("invalid memory limit %q", v)
}
