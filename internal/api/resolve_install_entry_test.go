package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"moo/internal/config"
	"moo/internal/source"
)

// 0.6.272 回归：跨源同宗更新（策略=同源/同宗）时，安装/下载条目必须
// 跟随卡片的 UpdateFromSource 源——用规范源会取到旧版被版本交叉校验
// 挡住（手动 409 / 自动更新空转）。

// newTwoSourceServer 双源同应用测试环境：
// nanhai31 有 golang 1.26.4，shuangji66 有 golang 1.27.1（同仓库不同写法）。
func newTwoSourceServer(t *testing.T) *Server {
	t.Helper()
	mk := func(fpk string) *httptest.Server {
		mux := http.NewServeMux()
		mux.HandleFunc("/fnpack.json", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"golang":{"version":"` + fpk + `","display_name":"Go","download_url":"https://github.com/shuangji66/Fndepot/releases/download/golang/golang.fpk"}}`))
		})
		ts := httptest.NewServer(mux)
		t.Cleanup(ts.Close)
		return ts
	}
	cfg := &config.Config{}
	m := source.NewManager(cfg)
	if _, err := m.AddSource("nanhai31", mk("1.26.4").URL+"/fnpack.json"); err != nil {
		t.Fatalf("AddSource nanhai31: %v", err)
	}
	if _, err := m.AddSource("shuangji66", mk("1.27.1").URL+"/fnpack.json"); err != nil {
		t.Fatalf("AddSource shuangji66: %v", err)
	}
	return &Server{Src: m, Cfg: cfg}
}

// seedCatalog 直接注入目录缓存（同包白盒），模拟 applyLineageUpdates 的产物。
func seedCatalog(s *Server, cards ...AppInfo) {
	s.catalogTTL = time.Hour
	s.catalogData = cards
	s.catalogAt = time.Now()
}

func TestResolveInstallEntry_CrossSourceRoutesToSibling(t *testing.T) {
	s := newTwoSourceServer(t)
	seedCatalog(s, AppInfo{
		Key: "golang@nanhai31", AppName: "golang", Source: "nanhai31",
		Installed: true, InstalledVersion: "1.26.4",
		HasUpdate: true, AvailableVersion: "1.27.1", UpdateFromSource: "shuangji66",
	})
	a, err := s.Src.GetByKey("golang@nanhai31")
	if err != nil {
		t.Fatalf("GetByKey: %v", err)
	}
	srcName, sa, err := s.resolveInstallEntry("golang@nanhai31", a.Source, a)
	if err != nil {
		t.Fatalf("resolveInstallEntry: %v", err)
	}
	if srcName != "shuangji66" {
		t.Fatalf("应路由到同宗源 shuangji66，实际 %q", srcName)
	}
	if sa.Version != "1.27.1" {
		t.Fatalf("安装条目应为同宗源最新版 1.27.1，实际 %q", sa.Version)
	}
}

func TestResolveInstallEntry_StrictNoChange(t *testing.T) {
	s := newTwoSourceServer(t)
	// 无 UpdateFromSource（strict / 自身源更新）→ 原样返回
	seedCatalog(s, AppInfo{
		Key: "golang@nanhai31", AppName: "golang", Source: "nanhai31",
		Installed: true, InstalledVersion: "1.26.3",
		HasUpdate: true, AvailableVersion: "1.26.4",
	})
	a, err := s.Src.GetByKey("golang@nanhai31")
	if err != nil {
		t.Fatalf("GetByKey: %v", err)
	}
	srcName, sa, err := s.resolveInstallEntry("golang@nanhai31", a.Source, a)
	if err != nil {
		t.Fatalf("resolveInstallEntry: %v", err)
	}
	if srcName != "nanhai31" || sa != a {
		t.Fatalf("strict 下应保持规范源，实际 %q", srcName)
	}
}

func TestResolveInstallEntry_SiblingGoneErrors(t *testing.T) {
	s := newTwoSourceServer(t)
	// 同宗源已被删除/条目消失 → 诚实报错（不假装规范源有新版）
	seedCatalog(s, AppInfo{
		Key: "golang@nanhai31", AppName: "golang", Source: "nanhai31",
		Installed: true, InstalledVersion: "1.26.4",
		HasUpdate: true, AvailableVersion: "1.27.1", UpdateFromSource: "已删除的源",
	})
	a, err := s.Src.GetByKey("golang@nanhai31")
	if err != nil {
		t.Fatalf("GetByKey: %v", err)
	}
	if _, _, err := s.resolveInstallEntry("golang@nanhai31", a.Source, a); err == nil {
		t.Fatal("同宗源不存在必须报错")
	}
}
