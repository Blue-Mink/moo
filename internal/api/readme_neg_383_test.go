package api

import (
	"testing"
	"time"
)

// 0.6.383 用户令：README 图片误判的负缓存必须保持短周期。
//
// 背景：0.6.382 收紧了「两条镜像 404 互票凑满 2 票判死」的口径，但只要判死一次，
// 30 分钟的负缓存会让详情页在半小时内都显示「图片源 404」。缩短到 5 分钟，
// 误判可在下一次访问时自动重试恢复。
func TestReadmeNegTTLStaysShort(t *testing.T) {
	if readmeNegTTL > 5*time.Minute {
		t.Fatalf("readmeNegTTL = %v，应 ≤ 5m（长时间负缓存会让一次误判挂很久）", readmeNegTTL)
	}
	if readmeNegTTL <= 0 {
		t.Fatalf("readmeNegTTL = %v，不能为 0（会退化为每次请求都重抓）", readmeNegTTL)
	}
	// 仍必须显著短于「内存层正缓存」，否则等于没有负缓存语义。
	if readmeNegTTL >= readmeMemTTL {
		t.Fatalf("readmeNegTTL(%v) 应短于 readmeMemTTL(%v)", readmeNegTTL, readmeMemTTL)
	}
}
