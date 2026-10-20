package api

// ghforks_round_test.go —— 0.6.326d H2（fork 自举可靠性）单测。
//
// 三件事各一条断言点：①每轮预算不得越匿名限流（ghForksBudget）；
// ②ghForkRepos 必须把「已知 fork parent」也排进队列（二层链，原实现只排源
// 列表 → 链只能发现一层），且去重、跳过非 GitHub 主机、脏缓存成环不死循环；
// ③源包侧的队列上限与负缓存 TTL 见 internal/source/ghforks_test.go。

import (
	"reflect"
	"testing"
	"time"

	"moo/internal/config"
	"moo/internal/source"
)

// 预算必须留在匿名 60 次/小时内：首轮 55，其后 12/20min = 36/h。
// 0.6.334：新增 token 档（5000/h）；0.6.340：token 档改为「待拉仓库一次给足」
// （下限 400），冷启动/丢缓存在一轮内补满，不再走多轮。
// 已知残余（非本测试可覆盖）：匿名首小时名义 55+12+12=79 > 60 —— 靠「304 不
// 计数额度」与「额满即 ErrRateLimited 提前停手」兜底，不会拉错数据，只是当轮
// 少拉几个（下轮继续）。稳态 36/h 才是长期约束。
func TestGHForksBudgetWithinAnonymousLimit(t *testing.T) {
	if got := ghForksBudget(true, false, 159); got != 55 {
		t.Fatalf("匿名首轮预算 = %d, want 55", got)
	}
	if got := ghForksBudget(false, false, 159); got != 12 {
		t.Fatalf("匿名常规轮预算 = %d, want 12", got)
	}
	if got := ghForksBudget(false, false, 159) * 3; got > 60 {
		t.Fatalf("匿名稳态每小时 %d 次 > 60（匿名上限）", got)
	}
	// token 档：额度 5000/h，一轮给足（0.6.340）
	if got := ghForksBudget(true, true, 159); got != 400 {
		t.Fatalf("token 首轮预算 = %d, want 400（下限）", got)
	}
	if got := ghForksBudget(false, true, 159); got != 400 {
		t.Fatalf("token 常规轮预算 = %d, want 400（下限）", got)
	}
	// 待拉仓库多于下限时按实际数放大（源暴涨后仍能一轮扫完）
	if got := ghForksBudget(false, true, 900); got != 900 {
		t.Fatalf("token 档大源集预算 = %d, want 900", got)
	}
	if got := ghForksBudget(false, true, 900); got > 5000 {
		t.Fatalf("token 档单轮 %d 次 > 5000（token 上限）", got)
	}
}

// ghForksInterval：本轮 0 个（队列空/全新鲜）→ 退避 6h；否则 token 档 15s、
// 匿名档 20min。退避规则是 0.6.334 的核心修复之一（旧实现永远 20min 空转）；
// 0.6.340 把 token 档从 1min 压到 15s（用户实测「溯源不够快」）。
func TestGHForksIntervalBackoff(t *testing.T) {
	if got := ghForksInterval(true, 0); got != 6*time.Hour {
		t.Fatalf("队列空应退避 6h, got %v", got)
	}
	if got := ghForksInterval(false, 0); got != 6*time.Hour {
		t.Fatalf("队列空应退避 6h（匿名）, got %v", got)
	}
	if got := ghForksInterval(true, 5); got != 15*time.Second {
		t.Fatalf("token 档间隔 = %v, want 15s", got)
	}
	if got := ghForksInterval(false, 5); got != 20*time.Minute {
		t.Fatalf("匿名档间隔 = %v, want 20m", got)
	}
}

// ghForkRepos：源仓 + 二层链 parent 全覆盖，去重、跳过非 GitHub、环安全。
func TestGHForkReposChain(t *testing.T) {
	newSrv := func(t *testing.T, urls ...[2]string) (*Server, *source.GHForks) {
		t.Helper()
		refs := make([]config.SourceRef, 0, len(urls))
		for _, u := range urls {
			refs = append(refs, config.SourceRef{Name: u[0], URL: u[1]})
		}
		cfg := &config.Config{Sources: refs}
		f := source.NewGHForks("")
		return &Server{Cfg: cfg, Src: source.NewManager(cfg), Forks: f}, f
	}

	// ① 普通源仓（非 fork）+ 一个非 GitHub 源（Gitea：没有 fork API）→ 只排前者
	s, f := newSrv(t,
		[2]string{"A", "https://github.com/a/fndepot"},
		[2]string{"内网 Gitea", "http://192.0.2.15:3033/bluemink/moo"},
	)
	f.SetForTest("a/fndepot", "")
	if got, want := s.ghForkRepos(), []string{"a/fndepot"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("① ghForkRepos = %#v, want %#v", got, want)
	}

	// ② parent 不在源列表：仍要排上（否则溯源链断在一层）
	s, f = newSrv(t, [2]string{"A", "https://github.com/Chordi0124/FnDepot"})
	f.SetForTest("chordi0124/fndepot", "EWEDLCM/FnDepot")
	if got, want := s.ghForkRepos(), []string{"chordi0124/fndepot", "ewedlcm/fndepot"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("② ghForkRepos = %#v, want %#v（parent 必须入队）", got, want)
	}

	// ③ 二层链：parent 也在源列表且自己还是 fork → 继续向上（去重，不重复列源仓）
	s, f = newSrv(t,
		[2]string{"A", "https://github.com/Chordi0124/FnDepot"},
		[2]string{"B", "https://github.com/EWEDLCM/FnDepot"},
	)
	f.SetForTest("chordi0124/fndepot", "ewedlcm/fndepot")
	f.SetForTest("ewedlcm/fndepot", "Root/Origin")
	want := []string{"chordi0124/fndepot", "ewedlcm/fndepot", "root/origin"}
	if got := s.ghForkRepos(); !reflect.DeepEqual(got, want) {
		t.Fatalf("③ ghForkRepos = %#v, want %#v（二层链）", got, want)
	}

	// ④ fork 关系未知 → 保守只排源仓本身（未知的不臆造 parent）
	s, _ = newSrv(t, [2]string{"A", "https://github.com/unknown/repo"})
	if got, want := s.ghForkRepos(), []string{"unknown/repo"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("④ ghForkRepos = %#v, want %#v", got, want)
	}

	// ⑤ 脏缓存成环：去重兜底，不死循环、不重复
	s, f = newSrv(t, [2]string{"A", "https://github.com/loop/a"})
	f.SetForTest("loop/a", "loop/b")
	f.SetForTest("loop/b", "loop/a")
	if got, want := s.ghForkRepos(), []string{"loop/a", "loop/b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("⑤ 关系环应去重收敛 = %#v, want %#v", got, want)
	}

	// ⑥ nil 安全（无源 / 空服务）
	var nilSrv *Server
	if got := nilSrv.ghForkRepos(); got != nil {
		t.Fatalf("nil Server 应返回 nil, got %#v", got)
	}
	if got := (&Server{}).ghForkRepos(); got != nil {
		t.Fatalf("无源应返回 nil, got %#v", got)
	}
}
