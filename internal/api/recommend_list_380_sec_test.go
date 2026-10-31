package api

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 0.6.380（门 2 审计 D2）：推荐清单地址默认只允许公共地址——
// 被指向内网 / 回环 / 云元数据端点时按安全策略拒绝（随后回落内嵌清单，
// 发现页不至于空白）；确有内网清单需求时用既有开关
// SourceListAllowPrivate（设置里的「允许自动同步内网源」）显式放开。
func TestRecommendListPrivateURLBlocked_380(t *testing.T) {
	s := recommendTestServer(t, nil, "http://10.1.2.3/recommend_list.txt", nil)
	if _, err := s.fetchRecommendListText(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "安全策略") {
		t.Fatalf("内网清单地址应被安全策略拒绝，实际 err=%v", err)
	}

	// 开关打开后不再被策略拦（连接失败属网络层，错误里不得再出现「安全策略」）
	s.Cfg.SourceListAllowPrivate = true
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := s.fetchRecommendListText(ctx); err != nil && strings.Contains(err.Error(), "安全策略") {
		t.Fatalf("开关打开后不应再被安全策略拒绝：%v", err)
	}
}

// 兜底：被策略拒绝时 /api/recommended 仍返回内嵌 4 条（不空、不报错）。
func TestRecommendListPrivateURLFallsBack_380(t *testing.T) {
	names := []string{"fnos-apps-store", "fndepot", "fn-knock", "moo-mate"}
	cat := make([]AppInfo, 0, len(names))
	for _, n := range names {
		cat = append(cat, recCard(n, "s"))
	}
	s := recommendTestServer(t, cat, "http://169.254.169.254/recommend_list.txt", nil)
	if got := recNames(z5Recommended(t, s, 12)); strings.Join(got, ",") != strings.Join(names, ",") {
		t.Fatalf("被拒后应回落内嵌清单 %v，实际 %v", names, got)
	}
}
