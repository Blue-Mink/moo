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

	"moo/internal/config"
	"moo/internal/source"
)

// 预算必须留在匿名 60 次/小时内：首轮 45（留 15 次余量给 ETag 复检与其它
// GitHub API 调用方），其后 12/20min = 36/h。
// 已知残余（非本测试可覆盖）：首小时名义 45+12+12=69 > 60 —— 靠「304 不计数
// 额度」与「额满即 ErrRateLimited 提前停手」兜底，不会拉错数据，只是当轮少拉
// 几个（下小时继续）。若未来要严格 ≤60，需把轮间隔从 20min 调大或首轮再降。
func TestGHForksBudgetWithinAnonymousLimit(t *testing.T) {
	if got := ghForksBudget(true); got != 45 {
		t.Fatalf("首轮预算 = %d, want 45", got)
	}
	if got := ghForksBudget(false); got != 12 {
		t.Fatalf("常规轮预算 = %d, want 12", got)
	}
	if ghForksBudget(true) > 60 {
		t.Fatal("首轮预算不得单独超匿名上限")
	}
	if got := ghForksBudget(false) * 3; got > 60 {
		t.Fatalf("稳态每小时 %d 次 > 60（匿名上限）", got)
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
