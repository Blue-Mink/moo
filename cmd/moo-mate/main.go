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
	"embed"
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

func main() {
	// FNOS 生命周期脚本注入的环境变量优先；flag 供本地调试兜底。
	listen := flag.String("listen", "127.0.0.1:38108", "Moo Mate 监听地址")
	mooBase := flag.String("moo-base", "unix:///vol1/@appcenter/moo/app.sock", "本机 Moo admin API base（unix://socket 或 http://base）")
	selfBase := flag.String("self-base", "http://127.0.0.1:38108", "Moo Mate 对外地址（写进加源 url）")
	token := flag.String("token", "", "GitHub token（可选，解除 60/h 限额）")
	dataFile := flag.String("data", "moo-mate-index.json", "索引持久化文件路径")
	flag.Parse()

	listenVal := envOr("MOO_MATE_LISTEN", *listen)
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

	c := newCompanion(mooBaseVal, selfBaseVal, tokenVal, web, newIndex(dataVal))
	c.bootDir = bootDir
	// 配置 + 加解密器：GitHub token 落盘 enc:v1（密钥文件 .moo-secret.key 在数据目录）。
	dataDir := filepath.Dir(dataVal)
	cfgPath := filepath.Join(dataDir, "config.json")
	cfg, _ := loadMateConfig(cfgPath)
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
	mux.HandleFunc("GET /api/icon/{appname}", c.handleIcon)
	mux.HandleFunc("GET /api/status", c.handleStatus)
	mux.HandleFunc("GET /api/index", c.handleListIndex)
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
	mux.HandleFunc("POST /api/softstore-sync", c.handleSoftstoreSync)
	mux.HandleFunc("DELETE /api/index/{appname}", c.handleDeleteApp)
	// 数据备份
	mux.HandleFunc("GET /api/datadir", c.handleDataDir)
	mux.HandleFunc("POST /api/datadir", c.handleDataDirSet)
	mux.HandleFunc("GET /api/dirs", c.handleDirs)
	mux.HandleFunc("GET /api/backup", c.handleBackup)
	mux.HandleFunc("GET /api/backup/download", c.handleBackupDownload)
	mux.HandleFunc("POST /api/backup", c.handleBackupWrite)
	mux.HandleFunc("GET /api/backups", c.handleBackupList)
	mux.HandleFunc("POST /api/backup/restore", c.handleBackupRestore)
	mux.HandleFunc("POST /api/backup/restore-upload", c.handleBackupRestoreUpload)
	mux.HandleFunc("POST /api/clear-data", c.handleClearData)
	// 非 API 路径回退到 embed 的静态文件（FileServer 自动提供 index.html）。
	mux.HandleFunc("/", web.ServeHTTP)

	log.Printf("Moo Mate 启动：%s（Moo base=%s，索引=%s）", listenVal, mooBaseVal, dataVal)

	// 软仓定时同步循环（首次启动稍作延迟后立即同步一次，之后按配置间隔）。
	go c.syncLoop()

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
			gw := http.StripPrefix(gwPrefix, mux)
			go func() {
				if err := http.Serve(uln, gw); err != nil {
					log.Printf("unix socket 服务异常: %v", err)
				}
			}()
		}
	}

	if err := http.ListenAndServe(listenVal, mux); err != nil {
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

// trySyncLog 跑一次完整同步（软仓 + 索引里全部应用的上游检查）并记日志（定时循环与唤醒共用）。
func (c *companion) trySyncLog() {
	softAdded, softUpdated, checked, updated, err := c.runFullSync()
	if err != nil {
		log.Printf("同步失败: %v", err)
		return
	}
	log.Printf("同步完成: 软仓新增 %d / 更新 %d，上游检查 %d / 更新 %d", softAdded, softUpdated, checked, updated)
}

// syncLoop 软仓定时同步循环：首次启动稍作延迟后立即同步一次，之后按配置间隔；
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
		}
	}
}
