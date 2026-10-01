package api

import "context"

type userCtxKey struct{}

// withUserCtx 把用户上下文写入 request context。
func withUserCtx(ctx context.Context, u User) context.Context {
	return context.WithValue(ctx, userCtxKey{}, u)
}

// userFrom 从 request context 读取用户上下文。
func userFrom(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(userCtxKey{}).(User)
	return u, ok
}
