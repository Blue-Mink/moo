// Package lang 目录语言跟随（0.6.269，M4 余项）。
//
// 官方目录接口（/app-center/v1/*）的 language 参数此前硬编码 zh-CN。
// 现在：请求上下文携带语言（Handler 中间件注入）——显式配置
// catalog_language 优先，否则跟随请求 Accept-Language（zh→zh-CN、
// en→en-US，其余回退 zh-CN）。后台协程无请求上下文，走配置值/默认。
package lang

import (
	"context"
	"strings"
)

// Default 目录缺省语言（与历史行为一致）。
const Default = "zh-CN"

// Supported 允许的语言集合（设置校验用）。
var Supported = map[string]bool{
	"auto":  true,
	"zh-CN": true,
	"en-US": true,
}

type ctxKey struct{}

// WithContext 把语言放进 ctx；空串原样返回。
func WithContext(ctx context.Context, l string) context.Context {
	if l == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, l)
}

// From 取 ctx 语言，缺省 zh-CN。
func From(ctx context.Context) string {
	if l, ok := ctx.Value(ctxKey{}).(string); ok && l != "" {
		return l
	}
	return Default
}

// FromAcceptLanguage 解析 Accept-Language 头：取第一个 tag，
// zh*→zh-CN、en*→en-US，其它一律回退 zh-CN（面板仅这两种可用）。
func FromAcceptLanguage(h string) string {
	for _, part := range strings.Split(h, ",") {
		tag := strings.ToLower(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]))
		if tag == "" {
			continue
		}
		if tag == "zh" || strings.HasPrefix(tag, "zh-") {
			return "zh-CN"
		}
		if tag == "en" || strings.HasPrefix(tag, "en-") {
			return "en-US"
		}
		return Default
	}
	return Default
}
