package netguard

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func resetNegCache() {
	negMu.Lock()
	negCache = map[string]negEntry{}
	negMu.Unlock()
}

// F2：解析失败负 TTL——首调走 resolver，TTL 内秒拒（不再调 resolver），
// 过期后重探；返回同一错误。
func TestPublicDNSNegTTL(t *testing.T) {
	origLookup, origTTL := dnsLookup, negTTL
	calls := 0
	dnsLookup = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		calls++
		return nil, errors.New("i/o timeout")
	}
	negTTL = 50 * time.Millisecond
	resetNegCache()
	defer func() {
		dnsLookup, negTTL = origLookup, origTTL
		resetNegCache()
	}()
	SetRebuilder(nil)

	_, err1 := Public("https://neg-host-a.invalid/x")
	if err1 == nil {
		t.Fatal("解析失败应拒绝")
	}
	if calls != 1 {
		t.Fatalf("首调应命中 resolver 1 次: %d", calls)
	}
	t1 := time.Now()
	_, err2 := Public("https://neg-host-a.invalid/x")
	fast := time.Since(t1)
	if calls != 1 {
		t.Fatalf("TTL 内负缓存命中，不应再调 resolver: %d", calls)
	}
	if err2 == nil || err2.Error() != err1.Error() {
		t.Fatalf("负缓存应返回同一错误: %v vs %v", err1, err2)
	}
	if fast > 10*time.Millisecond {
		t.Errorf("负缓存应秒拒（<10ms），实际 %v", fast)
	}
	// TTL 过期 → 重新探测
	time.Sleep(70 * time.Millisecond)
	_, err3 := Public("https://neg-host-a.invalid/x")
	if err3 == nil {
		t.Fatal("过期后仍应拒绝")
	}
	if calls != 2 {
		t.Fatalf("过期后应重探 resolver: calls=%d", calls)
	}
}

// 解析成功不缓存（防 IP 变化被旧值挡住）——每次都必须走 resolver。
func TestPublicSuccessNotCached(t *testing.T) {
	origLookup := dnsLookup
	n := 0
	dnsLookup = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		n++
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil // 公共 IP
	}
	resetNegCache()
	defer func() {
		dnsLookup = origLookup
		resetNegCache()
	}()
	SetRebuilder(nil)
	for i := 0; i < 3; i++ {
		if _, err := Public("https://example.com/x"); err != nil {
			t.Fatalf("公共 IP 应放行: %v", err)
		}
	}
	if n != 3 {
		t.Errorf("解析成功不得缓存（每次现解）: n=%d, want 3", n)
	}
}

// 解析成功但指向内网 IP = 实时判定路径，不写负缓存（安全语义零变化）。
func TestPublicInternalIPNotCached(t *testing.T) {
	origLookup := dnsLookup
	dnsLookup = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("172.16.0.50")}}, nil
	}
	resetNegCache()
	defer func() {
		dnsLookup = origLookup
		resetNegCache()
	}()
	SetRebuilder(nil)
	_, err := Public("https://internal-host.example/x")
	if err == nil {
		t.Fatal("解析到内网地址应拒绝")
	}
	negMu.Lock()
	leaked := len(negCache)
	negMu.Unlock()
	if leaked != 0 {
		t.Errorf("内网 IP 判定路径不得写负缓存: %d 条", leaked)
	}
}
