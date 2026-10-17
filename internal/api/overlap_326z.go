package api

// 0.6.326z：FnDepot 式「源重合率」——只读信号，绝不禁源。
//
// 背景：溯源（lineage.go）只能识别「发布 owner 不同 / fork 关系」的搬运，
// 对「同 owner 且 fork=false 的整仓复制源」是盲点（326d 审计实证 6 组非
// fork 双胞胎根仓，影响 9 源 13 卡）。重合率与 fork 标志无关，把这类重复
// 变成**源级可见信号**——是「让用户看得见」，不是「自动改结论」。
//
// 纪律（用户令，不得违反）：
//   - 绝不作为停源依据：不改任何源的启用/禁用语义、不新增开关、不加按钮；
//   - 不改溯源判定；不在应用详情页「溯源」行出现；
//   - 只做 appname / appname|version 两档（与 FnDepot 同口径，便于横向对照）；
//   - 官方源 OfficialSourceID 不参与（不作为被评估源，也不计入对方分母）。
//
// 口径（对齐 FnDepot 实测日志反推，见 OVERLAP_326Z_PLAN.md §2）：
//
//	name_ratio   = |appname(A) ∩ appname(B)| / max(|A|, |B|)
//	exact_ratio  = |(appname|version)(A) ∩ (appname|version)(B)| / max(|A|, |B|)
//	overlap_with = 使 max(name_ratio, exact_ratio) 最大者；平票取源名字典序小者
//	risk: high ≥0.80 / medium ≥0.50 / low ≥0.25 / none <0.25（阈值可调）
//	      ＋最小证据量 overlapMinEvidence（交集 ≥2，否则档位回落 none）

// SourceOverlap 某源相对「最相似对方源」的重合情况（源级只读信号）。
type SourceOverlap struct {
	With        string  // 最相似的对方源名（平票取源名字典序小者；证据不足时档位为 none，本字段仍填）
	NameCount   int     // |appname(A) ∩ appname(B)|
	ExactCount  int     // |(appname|version)(A) ∩ (appname|version)(B)|
	NameRatio   float64 // NameCount / max(|A|, |B|)
	ExactRatio  float64 // ExactCount / max(|A|, |B|)
	UniqueNames int     // |appname(A) \ appname(B)|（「停掉它会丢多少」）
	SelfCount   int     // |appname(A)|（比值分母依据）
	Risk        string  // none|low|medium|high（交集 < overlapMinEvidence 时恒为 none）
}

const (
	overlapRiskHigh   = 0.80
	overlapRiskMedium = 0.50
	overlapRiskLow    = 0.25
	// overlapMinEvidence 最小证据量（0.6.326z 门槛修正，用户令「先按门槛问题定方案再跑」）：
	// 交集（同名数 或 同名同版本数）至少 2 才给出档位——只共享 1 个 appname 不足以
	// 构成「重合」结论。现网 160 源实测：114 条信号里有 29 条交集=1，其中 26 条是
	// 「各只有 1 个应用的源互相指向」（比 100% 但信息量为零）；加此门槛后显示 85 条，
	// 且 326d 审计出的非 fork 双胞胎（boater-man↔WhoIsAisIohw、ylgyyy↔siilao，
	// 各 2 卡、交集 2）全部保留。证据不足时仅档位回落 none，其余字段照常下发。
	overlapMinEvidence = 2
)

// overlapRiskLevel 把比值映射为档位（阈值口径见文件头注释）。
func overlapRiskLevel(r float64) string {
	switch {
	case r >= overlapRiskHigh:
		return "high"
	case r >= overlapRiskMedium:
		return "medium"
	case r >= overlapRiskLow:
		return "low"
	}
	return "none"
}

// computeSourceOverlap 扫一遍目录，返回「源名 → 重合信号」（无信号的源不出现在表里）。
// 线性复杂度：一次建两张倒排表（appname→源集合、appname|version→源集合），只对
// 「同 key 出现在 ≥2 个源」的条目做 pair 累加——无 N² 枚举；2155 卡 / 160 源毫秒级。
func computeSourceOverlap(cards []AppInfo) map[string]SourceOverlap {
	names := map[string]map[string]struct{}{}  // 源 → appname 集合
	exacts := map[string]map[string]struct{}{} // 源 → appname|version 集合
	nameInv := map[string][]string{}           // appname → 源列表（源内去重）
	exactInv := map[string][]string{}          // appname|version → 源列表（源内去重）
	for _, c := range cards {
		src := c.Source
		if src == "" || src == OfficialSourceID {
			continue // 无源卡（系统自装）/ 官方源：不参与
		}
		overlapAddInv(names, nameInv, src, c.AppName)
		overlapAddInv(exacts, exactInv, src, c.AppName+"\x00"+c.LatestVersion)
	}
	pairName := map[string]map[string]int{}  // 源 → partner → 名称共 key 数
	pairExact := map[string]map[string]int{} // 源 → partner → 精确共 key 数
	for _, srcs := range nameInv {
		overlapCountPairs(pairName, srcs)
	}
	for _, srcs := range exactInv {
		overlapCountPairs(pairExact, srcs)
	}

	out := make(map[string]SourceOverlap, len(names))
	for src, set := range names {
		o := SourceOverlap{SelfCount: len(set), Risk: "none"}
		bestScore := -1.0
		seen := map[string]bool{}
		// 候选 partner = 名称或精确任一有交集者（两表 key 合一）
		others := make([]string, 0, len(pairName[src])+len(pairExact[src]))
		for _, m := range []map[string]int{pairName[src], pairExact[src]} {
			for other := range m {
				if !seen[other] {
					seen[other] = true
					others = append(others, other)
				}
			}
		}
		for _, other := range others {
			nc, ec := pairName[src][other], pairExact[src][other]
			den := o.SelfCount
			if n := len(names[other]); n > den {
				den = n // 分母取 max（FnDepot 实测口径）
			}
			if den == 0 {
				continue
			}
			nr := float64(nc) / float64(den)
			er := float64(ec) / float64(den)
			score := nr
			if er > score {
				score = er
			}
			// 严格全序 total order（分数降序 → 源名升序）⇒ 与 map 迭代序无关，结果确定
			if score < bestScore || (score == bestScore && other >= o.With) {
				continue
			}
			o.With, o.NameCount, o.ExactCount = other, nc, ec
			o.NameRatio, o.ExactRatio = nr, er
			bestScore = score
		}
		if bestScore > 0 {
			// 最小证据门槛（见 overlapMinEvidence）：交集不足 2 时档位保持 none，
			// 但 With / 各 ratio / 各 count 照常保留（数据零丢失，仅不下发视图）。
			if o.NameCount >= overlapMinEvidence || o.ExactCount >= overlapMinEvidence {
				o.Risk = overlapRiskLevel(bestScore)
			}
			if o.UniqueNames = o.SelfCount - o.NameCount; o.UniqueNames < 0 {
				o.UniqueNames = 0
			}
		}
		out[src] = o
	}
	return out
}

// overlapAddInv 把 (src, key) 记入倒排表；同源同 key 只记一次
// （源内重复卡不重复计数，保证 |A∩B| 与 |A| 都是集合语义）。
func overlapAddInv(sets map[string]map[string]struct{}, inv map[string][]string, src, key string) {
	set, ok := sets[src]
	if !ok {
		set = map[string]struct{}{}
		sets[src] = set
	}
	if _, dup := set[key]; dup {
		return
	}
	set[key] = struct{}{}
	inv[key] = append(inv[key], src)
}

// overlapCountPairs 对同一 key 的源列表做两两计数（k 通常为 2，无 N² 风险）。
func overlapCountPairs(pairs map[string]map[string]int, srcs []string) {
	for i := 0; i < len(srcs); i++ {
		for j := i + 1; j < len(srcs); j++ {
			overlapBump(pairs, srcs[i], srcs[j])
			overlapBump(pairs, srcs[j], srcs[i])
		}
	}
}

func overlapBump(pairs map[string]map[string]int, a, b string) {
	m, ok := pairs[a]
	if !ok {
		m = map[string]int{}
		pairs[a] = m
	}
	m[b]++
}

// overlapFor 返回某源的重合信号（目录未构建/无该源 → 零值，字段留空不报错）。
func (s *Server) overlapFor(name string) SourceOverlap {
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	return s.catalogOverlap[name]
}
