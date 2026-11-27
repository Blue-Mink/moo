package api

// dedup_determinism_4a_test.go —— 盲点补测 批次 4a · T1（FIXPLAN 盲点矩阵 #4）
//
// 目标：全搬运组 / 全 fork 无 upstream / 版本不可比 / 已装搬运卡 / 大小写变体，
// 五组表驱动断言「代表卡确定」——同一输入重复 20 次，结果必须逐字一致。
//
// 为什么"重复 20 次"能验确定性：applyDedupPolicy 内部唯一按 map 取的是
// groupIdx（分组索引），遍历用的是伴随的 groupNames 切片；Go 运行时每次进程/
// 循环触发 map 迭代顺序都会变，若实现改成直接 range map，则在 20 次内几乎必然
// 出现代表卡翻转（本文件的用例即会抓到）。
//
// 事实锚点（读代码得出，勿凭记忆改断言）：
//   - 阶梯见 store_catalog.go dedupRepClass：已装 0 / 官方 1 / 可装非搬运 2 /
//     可装搬运 3 / 不可装非搬运 4 / 不可装搬运 5；
//   - 阶梯同级 → 最高版本 → 下载量 → **切片顺序**（唯一兜底，确定性由
//     buildCatalog 的确定性排序保证）。

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// dedupSignature 一次判定的可观测结果：可见卡集合 / 隐藏卡集合 / 同名宿主。
func dedupSignature(out []AppInfo) string {
	var vis, hid []string
	rep := ""
	for i := range out {
		if out[i].Hidden {
			hid = append(hid, out[i].Key)
			continue
		}
		vis = append(vis, out[i].Key)
		if out[i].SameNameCount > 0 {
			rep = fmt.Sprintf("%s/%d", out[i].Key, out[i].SameNameCount)
		}
	}
	sort.Strings(vis)
	sort.Strings(hid)
	return "V[" + strings.Join(vis, ",") + "]H[" + strings.Join(hid, ",") + "]R[" + rep + "]"
}

// assertDeterministic 复制输入 20 次逐个跑 one 档；want 为空只验确定性。
func assertDeterministic(t *testing.T, name string, mk func() []AppInfo, want string) {
	t.Helper()
	first := ""
	for n := 0; n < 20; n++ {
		out := mk()
		applyDedupPolicy(out, "one")
		sig := dedupSignature(out)
		if n == 0 {
			first = sig
			continue
		}
		if sig != first {
			t.Fatalf("%s：第 %d 次结果与首次不一致（代表卡不得依赖 map 迭代顺序）\nfirst=%s\nnow  =%s", name, n, first, sig)
		}
	}
	if want != "" && first != want {
		t.Fatalf("%s：结果不符预期\nwant=%s\ngot =%s", name, want, first)
	}
}

// mkTransfer 造一张"搬运卡"：有 upstream_repo、来源是 fork，且发布仓库 != 上游。
func mkTransfer(key, appname, ver, src, repo, upstream string, dl int) AppInfo {
	a := mkDedupCard(key, appname, "Xfer App", src, ver, repo, "", "", localArch(), false, intPtr(dl))
	a.UpstreamRepo = upstream
	a.SourceIsFork = true
	return a
}

// ── 组 1：全搬运组 ────────────────────────────────────────────────────────
// 组内 4 张卡都是搬运包（UpstreamRepo 非空、都不是发布仓库），阶梯同级（3）；
// 代表卡必须由版本决定且每次一致（k1 = 2.0.0）。
func TestDedupDeterminism_AllTransferGroup(t *testing.T) {
	mk := func() []AppInfo {
		return []AppInfo{
			mkTransfer("k1", "xfer-a", "2.0.0", "src-1", "fork/one", "up/acme", 0),
			mkTransfer("k2", "xfer-a", "1.5.0", "src-2", "fork/two", "up/acme", 0),
			mkTransfer("k3", "xfer-a", "1.0.0", "src-3", "fork/three", "up/acme", 0),
			mkTransfer("k4", "xfer-a", "1.0.0", "src-4", "fork/four", "up/beta", 0),
		}
	}
	// 前提自检：四张卡都必须真的被判成"搬运"（否则本组名不副实）。
	for i, a := range mk() {
		if a.UpstreamRepo == "" || dedupRepClass(&a, localArch()) != 3 {
			t.Fatalf("第 %d 张卡前提不成立：upstream=%q class=%d", i, a.UpstreamRepo, dedupRepClass(&a, localArch()))
		}
	}
	assertDeterministic(t, "全搬运组（版本可判）", mk, "V[k1]H[k2,k3,k4]R[k1/4]")
}

// 组 1b：阶梯 ①~⑧ 全平（同源级别/同版本/同下载量）→ 只能落 ⑨「切片顺序」。
// 这是设计内的兜底（store_catalog.go 阶梯注释 ⑨），断言"给定输入确定"：
// 代表 = 切片首元素，重复 20 次不得漂移。若将来引入真随机 tie-break，本用例
// 会立刻抓到。
func TestDedupDeterminism_TieFallsBackToSliceOrder(t *testing.T) {
	mk := func() []AppInfo {
		return []AppInfo{
			mkTransfer("a1", "xfer-b", "1.0.0", "src-1", "fork/one", "up/acme", 7),
			mkTransfer("a2", "xfer-b", "1.0.0", "src-2", "fork/two", "up/acme", 7),
			mkTransfer("a3", "xfer-b", "1.0.0", "src-3", "fork/three", "up/acme", 7),
		}
	}
	assertDeterministic(t, "全搬运组（全平 → 切片顺序）", mk, "V[a1]H[a2,a3]R[a1/3]")
}

// ── 组 2：全是 fork 但**没有** upstream_repo ─────────────────────────────
// 与组 1 的关键区别：SourceIsFork=true 不参与降级（dedupRepClass 只看
// UpstreamRepo），四张卡都是 class 2 → 版本比较决定代表。
func TestDedupDeterminism_AllForkWithoutUpstream(t *testing.T) {
	mk := func() []AppInfo {
		out := []AppInfo{
			mkDedupCard("f1", "fork-a", "Fork A", "src-1", "1.0.0", "who/one", "", "", localArch(), false, intPtr(0)),
			mkDedupCard("f2", "fork-a", "Fork A", "src-2", "3.0.0", "who/two", "", "", localArch(), false, intPtr(0)),
			mkDedupCard("f3", "fork-a", "Fork A", "src-3", "2.0.0", "who/three", "", "", localArch(), false, intPtr(0)),
		}
		for i := range out {
			out[i].SourceIsFork = true
			out[i].UpstreamRepo = "" // 显式：无上游证据
		}
		return out
	}
	for i, a := range mk() {
		if a.UpstreamRepo != "" || dedupRepClass(&a, localArch()) != 2 {
			t.Fatalf("第 %d 张卡前提不成立：必须是无 upstream 的 class 2", i)
		}
	}
	assertDeterministic(t, "全 fork 无 upstream", mk, "V[f2]H[f1,f3]R[f2/3]")
}

// ── 组 3：版本号不可比（1.0.0 / v1.0.0-1 / 2024.1）───────────────────────
// 三种写法里有的能被 ParseVersion 解析、有的不能；不能解析时 compareVersions
// 返回 0（= 平手），阶梯继续往下走。这里锁两件事：①比较过程不 panic；
// ②代表卡确定（20 次一致）。
func TestDedupDeterminism_IncomparableVersions(t *testing.T) {
	vers := []string{"1.0.0", "v1.0.0-1", "2024.1"}
	// ① 纯比较：所有两两组合都不得 panic，返回值必须是 -1/0/1。
	for _, a := range vers {
		for _, b := range vers {
			got := func() (c int) {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("compareVersions(%q,%q) panic: %v", a, b, r)
					}
				}()
				return compareVersions(a, b)
			}()
			if got < -1 || got > 1 {
				t.Fatalf("compareVersions(%q,%q) 返回越界值 %d", a, b, got)
			}
		}
	}
	mk := func() []AppInfo {
		return []AppInfo{
			mkDedupCard("v1", "ver-a", "Ver A", "src-1", "1.0.0", "who/one", "", "", localArch(), false, intPtr(5)),
			mkDedupCard("v2", "ver-a", "Ver A", "src-2", "v1.0.0-1", "who/two", "", "", localArch(), false, intPtr(5)),
			mkDedupCard("v3", "ver-a", "Ver A", "src-3", "2024.1", "who/three", "", "", localArch(), false, intPtr(5)),
		}
	}
	out := mk()
	if oneVisible := applyDedupPolicy(out, "one"); oneVisible != 1 {
		t.Fatalf("每组只该留 1 张可见，实际 %d", oneVisible)
	}
	// 只锁确定性 + 组信息完整，不锁"谁该赢"（不可比版本的先后不是契约）。
	assertDeterministic(t, "版本不可比组", mk, "")
}

// ── 组 4：已装卡本身是搬运包 + 组内有非搬运的高版本 ─────────────────────
// 设计确认（DEDUP2_TODO.md）：阶梯 ①「已装规范卡」永远先于溯源级，
// 所以代表 = 已装卡，即便它是搬运包、且组里有非搬运的更高版本。
func TestDedupDeterminism_InstalledTransferWins(t *testing.T) {
	mk := func() []AppInfo {
		inst := mkTransfer("i1", "inst-x", "1.0.0", "src-a", "fork/mine", "up/acme", 0)
		inst.Installed = true
		return []AppInfo{
			inst,
			mkDedupCard("n1", "inst-x", "Inst X", "src-b", "9.9.9", "real/project", "", "", localArch(), false, intPtr(0)),
		}
	}
	out := mk()
	if dedupRepClass(&out[0], localArch()) != 0 || dedupRepClass(&out[1], localArch()) != 2 {
		t.Fatalf("前提不成立：已装搬运卡应 class 0，非搬运高版本应 class 2（实际 %d/%d）",
			dedupRepClass(&out[0], localArch()), dedupRepClass(&out[1], localArch()))
	}
	assertDeterministic(t, "已装搬运卡 vs 非搬运高版本", mk, "V[i1]H[n1]R[i1/2]")
}

// ── 组 5：同 appname 大小写变体（Foo / foo）在 one 档 ────────────────────
// 3b 行为（D3「只提示不合并」）：两个**字面不同**的名字各自成组 → 各留一张
// 可见卡；same_name_ci 计数 = 组外同桶变体数（各 1）。列表卡数量不变。
func TestDedupDeterminism_CaseVariantsKeepBothCards(t *testing.T) {
	mk := func() []AppInfo {
		return []AppInfo{
			mkDedupCard("c1", "Foo", "Foo", "src-1", "1.0.0", "who/one", "", "", localArch(), false, intPtr(0)),
			mkDedupCard("c2", "foo", "foo", "src-2", "1.0.0", "who/two", "", "", localArch(), false, intPtr(0)),
		}
	}
	out := mk()
	if oneVisible := applyDedupPolicy(out, "one"); oneVisible != 2 {
		t.Fatalf("大小写变体不得合并：应留 2 张可见，实际 %d", oneVisible)
	}
	for i := range out {
		if out[i].Hidden {
			t.Fatalf("大小写变体不得被隐藏：%s", out[i].Key)
		}
		if out[i].SameNameCICount != 1 {
			t.Fatalf("%s 的 same_name_ci 计数应为 1，实际 %d", out[i].Key, out[i].SameNameCICount)
		}
		if out[i].SameNameCount != 0 {
			t.Fatalf("%s 是字面唯一组，SameNameCount 应为 0，实际 %d", out[i].Key, out[i].SameNameCount)
		}
	}
	// 三写（Foo/foo/FOO）→ 每张卡的变体计数 2，可见仍是 3 张。
	out3 := append(mk(), mkDedupCard("c3", "FOO", "FOO", "src-3", "1.0.0", "who/three", "", "", localArch(), false, intPtr(0)))
	if oneVisible := applyDedupPolicy(out3, "one"); oneVisible != 3 {
		t.Fatalf("三种大小写应各留一张，实际可见 %d", oneVisible)
	}
	for i := range out3 {
		if out3[i].SameNameCICount != 2 {
			t.Fatalf("%s 的 same_name_ci 计数应为 2，实际 %d", out3[i].Key, out3[i].SameNameCICount)
		}
	}
	assertDeterministic(t, "大小写变体组", mk, "V[c1,c2]H[]R[]")
}
