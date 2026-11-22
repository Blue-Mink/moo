package api

import (
	"runtime"
	"strconv"
	"sync"
	"testing"

	"moo/internal/config"
)

// TestCfgSwapConcurrent 覆盖备份恢复热替换（运行期整配置替换）与并发无锁
// 读者之间的竞态：读者一律经 s.cfg() 取指针，写方一律经 s.setCfg。
// 期望：-race 下无 DATA RACE；读者只会看到「某一次写入的完整配置」。
func TestCfgSwapConcurrent(t *testing.T) {
	const (
		readers = 8
		writes  = 800
	)
	s := &Server{Cfg: &config.Config{WebPort: "1"}}

	stop := make(chan struct{})
	ready := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ready // 等读者全部就绪，尽量与写循环重叠
			for {
				select {
				case <-stop:
					return
				default:
				}
				c := s.cfg()
				if c == nil {
					t.Errorf("cfg() 不应返回 nil")
					return
				}
				if c.WebPort == "" {
					t.Errorf("读到空端口")
					return
				}
				runtime.Gosched()
			}
		}()
	}
	close(ready)

	last := "1"
	for i := 1; i <= writes; i++ {
		last = strconv.Itoa(1000 + i)
		s.setCfg(&config.Config{WebPort: last})
	}
	close(stop)
	wg.Wait()

	if got := s.cfg().WebPort; got != last {
		t.Fatalf("热替换后应读到最后一次写入 %q，实得 %q", last, got)
	}
	// 设计约束：Cfg 是构造期快照，运行期只读，热替换只换 cfgPtr。
	if got := s.Cfg.WebPort; got != "1" {
		t.Fatalf("运行期不得改写构造期 Cfg 快照，WebPort=%q", got)
	}
}

// TestCfgNilSafety 固定 cfg()/setCfg 的 nil 语义（nil 接收者不 panic）。
func TestCfgNilSafety(t *testing.T) {
	var s *Server
	if s.cfg() != nil {
		t.Fatalf("nil Server 的 cfg() 应为 nil")
	}
	s.setCfg(&config.Config{WebPort: "2"}) // 不得 panic
	if (*Server)(nil).cfg() != nil {
		t.Fatalf("nil Server 的 cfg() 应为 nil")
	}
	if got := (&Server{}).cfg(); got != nil {
		t.Fatalf("未注入配置的 cfg() 应为 nil，实得 %+v", got)
	}
}
