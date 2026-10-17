package api

// 0.6.326 A1：未声明 README 的惰性自动探测从「20s 预算 + 瞬态失败不缓存」
// 改为「5s 预算 + 瞬态失败 10min 短负缓存」。实测依据见 proxyReadmeAuto 注释。

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"moo/internal/source"
)

// 预算与 TTL 的硬约束：任何回退到 20s / 10s / 10min 的改动都要在这里先红。
func TestReadmeAutoBudget_A1(t *testing.T) {
	if readmeAutoBudget > 5*time.Second {
		t.Fatalf("自动探测总预算应 ≤5s（实测原 20s 让 86%% 的卡首开卡满 20s），got %v", readmeAutoBudget)
	}
	if readmeAutoStage1 > 3*time.Second {
		t.Fatalf("阶段 1 预算应 ≤3s，got %v", readmeAutoStage1)
	}
	if readmeAutoStage1 >= readmeAutoBudget {
		t.Fatalf("阶段 1 必须留出镜像兜底预算：stage1=%v budget=%v", readmeAutoStage1, readmeAutoBudget)
	}
	if readmeAutoNegTTL != 30*time.Second {
		t.Fatalf("瞬态失败短负缓存应为 30s（残余 4：原 10min 把偶发挂起当「没有 README」隐藏 10 分钟），got %v", readmeAutoNegTTL)
	}
	if readmeAutoNegTTL >= readmeNegTTL {
		t.Fatalf("瞬态 TTL 必须短于确定性 404 的 TTL：%v >= %v", readmeAutoNegTTL, readmeNegTTL)
	}
	// 残余 3：声明 readme_url 的卡预算收紧（原 25s）。
	if readmeDeclBudget > 8*time.Second {
		t.Fatalf("声明 readme_url 的卡预算应 ≤8s（原 25s 让上游挂起时卡满 24s），got %v", readmeDeclBudget)
	}
	if readmeDeclBudget <= readmeAutoBudget {
		t.Fatalf("声明 readme_url 的预算应略宽于自动探测（README 确定存在）：decl=%v auto=%v", readmeDeclBudget, readmeAutoBudget)
	}
}

func negExpiry(t *testing.T, st *readmeStore, key string) time.Time {
	t.Helper()
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.neg[key]
}

// 自定义 TTL 生效，且默认 MarkNegative 仍是 30min（两条路径不能互相污染）。
func TestReadmeNegativeTTL_A1(t *testing.T) {
	st := newReadmeStore("")
	st.markNegativeTTL("short", readmeAutoNegTTL)
	if !st.IsNegative("short") {
		t.Fatal("短 TTL 负缓存应立即生效")
	}
	if d := time.Until(negExpiry(t, st, "short")); d < readmeAutoNegTTL-time.Second || d > readmeAutoNegTTL+time.Second {
		t.Fatalf("短 TTL 时长不对: %v", d)
	}
	st.MarkNegative("long")
	if d := time.Until(negExpiry(t, st, "long")); d < readmeNegTTL-time.Minute {
		t.Fatalf("确定性 404 仍应为 30min，got %v", d)
	}
	st.markNegativeTTL("zero", 0)
	if st.IsNegative("zero") {
		t.Fatal("ttl<=0 不应写入负缓存")
	}
}

// 瞬态失败落短负缓存后，同一应用再开详情必须零网络秒回（这是 A1 的第二半：
// 没有它，raw 挂起窗口里每次打开都重新付一遍探测预算）。
func TestProxyReadmeAutoNegativeHitFast_A1(t *testing.T) {
	s := &Server{}
	st := s.readmeStore()
	const key = "auto-readme|demo-src@demo-app@1.0.0"
	st.markNegativeTTL(key, readmeAutoNegTTL)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/apps/demo-app@demo-src/asset?type=readme", nil)
	start := time.Now()
	s.proxyReadmeAuto(rec, req, &source.App{Source: "demo-src", Name: "demo-app", Version: "1.0.0"})
	elapsed := time.Since(start)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("负缓存命中应回 404（前端据此隐藏 README 块），got %d", rec.Code)
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("负缓存命中必须零网络秒回，实际 %v", elapsed)
	}
}
