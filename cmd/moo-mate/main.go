// Command moo-mate 是 Moo Mate：从第三方 GitHub 仓库发现飞牛应用，
// 生成 moo.json 并托管在自身 HTTP 端口，自动加源到本机 Moo（免上传 GitHub）。
//
// 用法：
//
//	moo-mate -listen 127.0.0.1:38108 \
//	  -moo-base http://127.0.0.1:13813/app/moo \
//	  -self-base http://127.0.0.1:38108
//
// 流程：打开 http://127.0.0.1:38108/ → 贴仓库链接 → 探测 → 发现 → 生成 →
// 一键加源到 Moo（moJSON 由伴侣自己托管，不加源到 GitHub 仓库）。
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"moo/internal/secret"
)

//go:embed web/index.html
var webFS embed.FS

// envOr 取环境变量，空则回落默认值。
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// listenGuard 0.2.54（MM-01a/B4）：Mate 没有登录鉴权，监听地址默认必须是回环。
// 返回 (fatalMsg, warnMsg)：fatal 非空 → 拒绝启动；warn 非空 → 仅告警（已显式放行）。
func listenGuard(listen string, allowRemote bool) (fatalMsg, warnMsg string) {
	listen = strings.TrimSpace(listen)
	if isLoopbackListen(listen) {
		return "", ""
	}
	if allowRemote {
		return "", "监听地址 " + listen + " 不是回环地址（已按 MOO_MATE_ALLOW_REMOTE=1 放行）；Moo Mate 无内置登录，请勿直接暴露到公网/不可信局域网"
	}
	return "监听地址 " + listen + " 不是回环地址（Moo Mate 无内置登录，只应本机访问）。如确需开放，请设置 MOO_MATE_ALLOW_REMOTE=1", ""
}

func main() {
	// FNOS 生命周期脚本注入的环境变量优先；flag 供本地调试兜底。
	listen := flag.String("listen", "127.0.0.1:38108", "Moo Mate 监听地址")
	mooBase := flag.String("moo-base", "unix:///vol1/@appcenter/moo/app.sock", "本机 Moo admin API base（unix://socket 或 http://base）")
	selfBase := flag.String("self-base", "http://127.0.0.1:38108", "Moo Mate 对外地址（写进加源 url）")
	token := flag.String("token", "", "GitHub token（可选，解除 60/h 限额）")
	dataFile := flag.String("data", "moo-mate-index.json", "索引持久化文件路径")
	// 0.2.35：CI 夜间复检用的「客户端同款验收」入口（只读，验收完直接退出，不启动服务）。
	verifyRelease := flag.String("verify-release", "", "只做发布包验收：版本号，或 auto=读索引（校验完退出）")
	verifyArch := flag.String("verify-arch", "x86", "配合 -verify-release 的架构：x86 / arm / both")
	verifyIndex := flag.String("verify-index", "moo.json", "配合 -verify-release 的索引文件路径")
	flag.Parse()

	if strings.TrimSpace(*verifyRelease) != "" {
		os.Exit(runReleaseSelfCheck(*verifyRelease, *verifyArch, *verifyIndex))
	}

	listenVal := envOr("MOO_MATE_LISTEN", *listen)
	// 0.2.54（MM-01a）：Mate 无内置登录，默认只允许回环监听；误绑 0.0.0.0 直接拒绝，
	// 确需开放必须显式 MOO_MATE_ALLOW_REMOTE=1（B4：此时打 WARN，不静默）。
	if fatalMsg, warnMsg := listenGuard(listenVal, envOr("MOO_MATE_ALLOW_REMOTE", "") != ""); fatalMsg != "" || warnMsg != "" {
		if fatalMsg != "" {
			log.Fatalf("拒绝启动：%s", fatalMsg)
		}
		log.Printf("警告：%s", warnMsg)
	}
	mooBaseVal := envOr("MOO_MATE_MOO_BASE", *mooBase)
	selfBaseVal := envOr("MOO_MATE_SELF_BASE", *selfBase)
	tokenVal := envOr("MOO_MATE_TOKEN", *token)
	dataVal := envOr("MOO_MATE_DATA", *dataFile)
	// 0.2.29：数据目录指针（设置页改过「数据路径」后，ctl 脚本与这里都按它启动）。
	// 指针文件放在固定引导目录（默认 = 安装时数据目录），内容是一行绝对路径。
	bootDir := envOr("MOO_MATE_BOOTSTRAP", filepath.Dir(dataVal))
	if b, err := os.ReadFile(filepath.Join(bootDir, "datadir")); err == nil {
		if p := strings.TrimSpace(string(b)); p != "" && strings.HasPrefix(p, "/vol") {
			dataVal = filepath.Join(p, "index.json")
		}
	}

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("embed web 失败: %v", err)
	}
	web := http.FileServer(http.FS(sub))

	// 0.2.35：首次启动（索引文件不存在）写入出厂默认索引；已有数据一律不覆盖。
	// 0.2.37：默认列表优先读远程清单（配置可覆盖），失败回落内置快照。
	// 放在启动路径而不是 newIndex 内，避免污染单测里「空索引」的语义。
	dataDir := filepath.Dir(dataVal)
	cfgPath := filepath.Join(dataDir, "config.json")
	cfg, _ := loadMateConfig(cfgPath)
	bootClient := &http.Client{Timeout: 25 * time.Second}
	if n, src, err := bootstrapDefaultIndex(dataVal, cfg.defaultIndexURL(), bootClient); err != nil {
		logf("默认应用列表写入失败（继续空索引启动）：%v", err)
	} else if n > 0 {
		logf("已写入默认应用列表 %d 条（首次启动，来源=%s）", n, src)
	}
	c := newCompanion(mooBaseVal, selfBaseVal, tokenVal, web, newIndex(dataVal))
	c.bootDir = bootDir
	// 0.2.61：空索引兜底。历史版本可能把索引清成空文件，
	// bootstrapDefaultIndex 因 index.json 已存在而不再执行 → 列表永远为空且无处恢复。
	// 这里补一次：远端清单优先，失败回落内置快照；用户主动「清除同步索引」留了
	// 标记则跳过（保持清除语义）。已有条目一律不动。
	if n, src, serr := c.idx.seedDefaultIfEmpty(cfg.defaultIndexURL(), bootClient); serr != nil {
		log.Printf("补写默认应用列表失败（继续空索引启动）：%v", serr)
	} else if n > 0 {
		log.Printf("已补写默认应用列表 %d 条（来源=%s）", n, src)
	}
	// 0.2.35：自更新下载复用 Moo 的 GitHub 加速镜像表（GET /api/mate/mirrors）。
	mateMirrorProvider = c.mateMirrorPrefixes
	// 配置 + 加解密器：GitHub token 落盘 enc:v1（密钥文件 .moo-secret.key 在数据目录）。
	sealer, serr := secret.LoadOrCreate(dataDir)
	if serr != nil {
		log.Printf("凭据加解密器不可用（token 将拒绝存储）: %v", serr)
	}
	c.setConfig(cfgPath, cfg, sealer)

	mux := http.NewServeMux()
	// ⚠️ Go ServeMux 通配符必须占完整段：「/moo/{appname}.json」会在
	// HandleFunc 时 panic（invalid pattern）。托管路径用 /moo/{appname}，
	// .json 后缀在 handleHost 里剥（appHostURL 写进加源 url 的形态带 .json，
	// 让 Moo buildCandidates 走直链单候选，不当仓库根展开）。
	mux.HandleFunc("GET /moo/{appname}", c.handleHost)
	// 0.2.62：按 Moo 的仓库布局托管图标（/moo/<app>/ICON_256.PNG 等）——
	// Moo 对「外部源」卡片的图标候选就是这样拼的，Mate 此前只提供
	// /api/icon/<app>，于是全部 404、卡片只剩首字母占位。
	mux.HandleFunc("GET /moo/{appname}/{file}", c.handleHostedIcon)
	mux.HandleFunc("GET /api/icon/{appname}", c.handleIcon)
	mux.HandleFunc("GET /api/status", c.handleStatus)
	mux.HandleFunc("GET /api/update-check", c.handleUpdateCheck)
	mux.HandleFunc("GET /api/changelog", c.handleChangelog)
	// 应用内自更新（下载 + sha256 校验 + appcenter daemon 就地升级）
	mux.HandleFunc("POST /api/self-update", c.handleSelfUpdate)
	mux.HandleFunc("GET /api/self-update/status", c.handleSelfUpdateStatus)
	mux.HandleFunc("GET /api/index", c.handleListIndex)
	// 0.2.38：设置页「日志」卡片（查看 / 按天清理；轮转在 StartLogRotator 后台跑）
	mux.HandleFunc("GET /api/logs", c.handleLogs)
	mux.HandleFunc("POST /api/logs/cleanup", c.handleLogsCleanup)
	mux.HandleFunc("GET /api/config", c.handleConfigGet)
	mux.HandleFunc("PUT /api/config", c.handleConfigPut)
	mux.HandleFunc("POST /api/probe", c.handleProbe)
	mux.HandleFunc("POST /api/discover", c.handleDiscover)
	mux.HandleFunc("POST /api/build", c.handleBuild)
	mux.HandleFunc("POST /api/add-direct", c.handleAddDirect)
	mux.HandleFunc("POST /api/sync-app", c.handleSyncApp)
	mux.HandleFunc("POST /api/unsync-app", c.handleUnsyncApp)
	mux.HandleFunc("POST /api/batch-sync", c.handleBatchSync)
	mux.HandleFunc("POST /api/batch-delete", c.handleBatchDelete)
	mux.HandleFunc("POST /api/check-update", c.handleCheckUpdate)
	mux.HandleFunc("POST /api/ack-updates", c.handleAckUpdates)
	mux.HandleFunc("POST /api/sync-now", c.handleSyncNow)
	mux.HandleFunc("DELETE /api/index/{appname}", c.handleDeleteApp)
	// 数据备份
	mux.HandleFunc("GET /api/datadir", c.handleDataDir)
	mux.HandleFunc("POST /api/datadir", c.handleDataDirSet)
	mux.HandleFunc("GET /api/dirs", c.handleDirs)
	mux.HandleFunc("GET /api/backup", c.handleBackup)
	mux.HandleFunc("GET /api/backup/download", c.handleBackupDownload)
	mux.HandleFunc("POST /api/backup", c.handleBackupWrite)
	mux.HandleFunc("GET /api/backups", c.handleBackupList)
	mux.HandleFunc("DELETE /api/backup", c.handleBackupDelete)
	mux.HandleFunc("POST /api/backup/restore", c.handleBackupRestore)
	mux.HandleFunc("POST /api/backup/restore-upload", c.handleBackupRestoreUpload)
	mux.HandleFunc("POST /api/clear-data", c.handleClearData)
	mux.HandleFunc("GET /api/cache-stats", c.handleCacheStats)
	mux.HandleFunc("POST /api/cache/clean", c.handleCacheClean)
	// 非 API 路径回退到 embed 的静态文件（FileServer 自动提供 index.html）。
	mux.HandleFunc("/", web.ServeHTTP)

	// MM-02（0.2.52）：非安全方法统一过 CSRF 防线（Origin/Sec-Fetch-Site/
	// Content-Type + TCP 通道 Host 白名单）。TCP 与面板网关各用一套策略。
	tcpPolicy, gwPolicy := newCSRFPolicies(listenVal, c.base)
	log.Printf("CSRF 防护：host 白名单=%v（TCP loopbackOnly=%v）", tcpPolicy.allowedHosts, tcpPolicy.loopbackOnly)

	log.Printf("Moo Mate 启动：%s（Moo base=%s，索引=%s）", listenVal, mooBaseVal, dataVal)

	// 定时同步循环（首次启动稍作延迟后立即同步一次，之后按配置间隔）。
	go c.syncLoop()

	// 0.2.38：日志轮转（5MB 阈值 × 5 份归档，10 分钟检查一次；与清理串行）。
	go c.StartLogRotator(context.Background())

	// 0.2.34：端口变更后修复 Moo 里已添加的 Moo Mate 源地址（旧的回环端口会 404）。
	// 后台延迟重试（Moo 可能与本应用同时启动），失败不阻塞。
	go func() {
		for _, d := range []time.Duration{3 * time.Second, 15 * time.Second, 45 * time.Second} {
			time.Sleep(d)
			fixed, err := c.reconcileSourceURLs()
			if err != nil {
				log.Printf("源地址校验失败（稍后重试）: %v", err)
				continue
			}
			if fixed > 0 {
				log.Printf("已把 %d 个 Moo Mate 源地址更新到当前端口（%s）", fixed, c.base)
			}
			return
		}
	}()

	// 版本探测预热（启动后 3s 后台查一次，首个 /api/update-check 直接命中缓存）。
	c.warmMateUpdate()

	// 自更新需要 release 资产列表，复用 Moo 侧下发的 GitHub token。
	mateTokenFn = func() string {
		tok, _, err := c.githubTokenWithSource()
		if err != nil {
			return ""
		}
		return tok
	}

	// 自动备份循环（参考 Moo：每 N 天写一份，同目录保留最近 20 份）。
	go c.backupLoop()

	// 统一网关（与 Moo 同访问方式）：若注入 MOO_MATE_SOCKET（unix socket
	// 路径），在上面额外服务 Web UI——面板网关按 /app/moo-mate 前缀转发，
	// 剥离前缀后交给同一 mux。TCP 38108 保持不变（本机 Moo 拉 moo.json 的通道）。
	if sock := os.Getenv("MOO_MATE_SOCKET"); sock != "" {
		gwPrefix := "/app/moo-mate"
		if v := os.Getenv("MOO_MATE_GATEWAY_PREFIX"); v != "" {
			gwPrefix = v
		}
		_ = os.Remove(sock) // 清掉上次残留
		uln, uerr := net.Listen("unix", sock)
		if uerr != nil {
			log.Printf("unix socket 监听失败（网关入口不可用，继续 TCP）: %v", uerr)
		} else {
			_ = os.Chmod(sock, 0o700)
			log.Printf("unix socket 监听: %s（网关前缀 %s）", sock, gwPrefix)
			gw := http.StripPrefix(gwPrefix, csrfGuard(mux, gwPolicy))
			gwSrv := newHTTPServer(gw)
			go func() {
				if err := gwSrv.Serve(uln); err != nil {
					log.Printf("unix socket 服务异常: %v", err)
				}
			}()
		}
	}

	// 0.2.53（A3）：统一 HTTP 基础防护（读头/空闲超时、头大小）。
	tcpSrv := newHTTPServer(csrfGuard(mux, tcpPolicy))
	tcpSrv.Addr = listenVal
	if err := tcpSrv.ListenAndServe(); err != nil {
		log.Fatalf("服务启动失败: %v", err)
	}
}

// intervalDuration 把配置的同步间隔转成 time.Duration；"manual"/空 → 0（不自动）。
func (c *companion) intervalDuration() time.Duration {
	v := c.cfg.syncInterval()
	if v == "" || v == "manual" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return time.Hour
	}
	return d
}

// wakeSync 唤醒同步循环（配置变更/手动触发时调用，非阻塞）。
func (c *companion) wakeSync() {
	select {
	case c.syncWake <- struct{}{}:
	default:
	}
}

// trySyncLog 跑一次完整同步（索引里全部应用的上游检查）并记日志（定时循环与唤醒共用）。
func (c *companion) trySyncLog() {
	checked, updated, err := c.runFullSync()
	if errors.Is(err, errSyncBusy) {
		log.Printf("同步进行中，跳过本轮唤醒")
		return
	}
	if err != nil {
		log.Printf("同步失败: %v", err)
		return
	}
	log.Printf("同步完成: 上游检查 %d / 更新 %d", checked, updated)
}

// syncLoop 定时同步循环：首次启动稍作延迟后立即同步一次，之后按配置间隔；
// 间隔改为 manual 时挂起等唤醒信号；配置变更经 wakeSync 立即重排。
func (c *companion) syncLoop() {
	time.Sleep(3 * time.Second)
	c.trySyncLog()
	for {
		d := c.intervalDuration()
		if d <= 0 {
			<-c.syncWake
			c.trySyncLog()
			continue
		}
		t := time.NewTimer(d)
		select {
		case <-t.C:
			c.trySyncLog()
		case <-c.syncWake:
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
			// 0.2.62：唤醒必须真的跑一轮同步。此前这里只停掉定时器就回到循环开头，
			// 「同步更新」按钮等于没生效（实测点完 13 分钟无同步日志、Moo 源 app_count 不刷新）。
			c.trySyncLog()
		}
	}
}
