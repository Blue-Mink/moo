package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"moo/internal/notify"
	"moo/internal/source"
)

// 通知事件产生点（0.6.121）：
//
//	B 收藏应用有更新 / D1 源同步失败摘要 / D2 源同步恢复 / E1 目录异常下跌
//	← sourceRoundNotify：挂在源自动刷新周期（StartSourceAutoRefresh，6-24h）之后
//	C3 资源占用告警 / C4 后端健康异常  ← startSelfMonitor：5 分钟自检
//	D3 GitHub 加速源全部不可用        ← StartMirrorProbeLoop 每轮测速后检查
//	D4 Docker 加速源全部不可用（0.6.188）← dk 探测循环每轮测速后检查（与 D3 对偶）
//
// 去重/冷却状态持久化在 config.json（重启不重复告警）。

// sourceRoundNotify 源刷新轮次后的通知检查（调用方保证刷新已完成）。
func (s *Server) sourceRoundNotify(sts []source.SourceStatus) {
	// D1 源同步失败摘要
	type failRow struct{ name, err string }
	var failed []failRow
	total := 0
	for _, st := range sts {
		total += st.Count
		if st.Error != "" {
			failed = append(failed, failRow{st.Name, st.Error})
		}
	}
	sort.Slice(failed, func(i, j int) bool { return failed[i].name < failed[j].name })
	if len(failed) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "本轮 %d 个源成功 / %d 个失败\n", len(sts)-len(failed), len(failed))
		b.WriteString("失败源：")
		for i, f := range failed {
			if i >= 5 {
				fmt.Fprintf(&b, " 等 %d 个", len(failed))
				break
			}
			if i > 0 {
				b.WriteString("、")
			}
			b.WriteString(f.name)
		}
		// 0.6.144：markdown_v2 表格版（源 | 失败原因）
		var tb strings.Builder
		fmt.Fprintf(&tb, "本轮 %d 个源成功 / %d 个失败\n\n| 源 | 失败原因 |\n| :-- | :-- |\n",
			len(sts)-len(failed), len(failed))
		for i, f := range failed {
			if i >= 5 {
				fmt.Fprintf(&tb, "| … | 等 %d 个 |\n", len(failed))
				break
			}
			fmt.Fprintf(&tb, "| %s | %s |\n", mdCell(f.name), mdCell(f.err))
		}
		// 0.6.144：卡片两列列表（源 | 失败原因，最多 6 行）
		rows := make([]notify.CardRow, 0, len(failed))
		for i, f := range failed {
			if i >= 6 {
				break
			}
			rows = append(rows, notify.CardRow{Key: f.name, Value: f.err})
		}
		// 0.6.175 通知信息长度变体：简洁=只报失败数；完整=全部失败源+原因不截断。
		var bFull, tbFull strings.Builder
		fmt.Fprintf(&bFull, "本轮 %d 个源成功 / %d 个失败\n", len(sts)-len(failed), len(failed))
		for _, f := range failed {
			fmt.Fprintf(&bFull, "- %s：%s\n", f.name, f.err)
		}
		fmt.Fprintf(&tbFull, "本轮 %d 个源成功 / %d 个失败\n\n| 源 | 失败原因 |\n| :-- | :-- |\n",
			len(sts)-len(failed), len(failed))
		var rowsFull []notify.CardRow
		for i, f := range failed {
			fmt.Fprintf(&tbFull, "| %s | %s |\n", mdCell(f.name), mdCell(f.err))
			if i < 8 {
				rowsFull = append(rowsFull, notify.CardRow{Key: f.name, Value: f.err})
			}
		}
		s.notifyEventRichV("source_sync_failed", "源同步失败", b.String(), tb.String(), rows, "", "", false,
			&notify.Variants{
				ContentConcise: fmt.Sprintf("本轮 %d 个源同步失败", len(failed)),
				ContentFull:    bFull.String(),
				TableFull:      tbFull.String(),
				RowsFull:       rowsFull,
			})
	}

	// D2 源同步恢复（上轮失败 → 本轮成功）
	if s.Cfg.NotifyPrevFailed != nil && len(s.Cfg.NotifyPrevFailed) > 0 {
		failedNow := make(map[string]bool, len(failed))
		for _, f := range failed {
			failedNow[f.name] = true
		}
		var recovered []string
		for n := range s.Cfg.NotifyPrevFailed {
			if !failedNow[n] {
				recovered = append(recovered, n)
			}
		}
		sort.Strings(recovered)
		if len(recovered) > 0 {
			var b strings.Builder
			fmt.Fprintf(&b, "%d 个源恢复同步：", len(recovered))
			for i, n := range recovered {
				if i >= 5 {
					fmt.Fprintf(&b, " 等 %d 个", len(recovered))
					break
				}
				if i > 0 {
					b.WriteString("、")
				}
				b.WriteString(n)
			}
			s.notifyEvent("source_recovered", "源同步恢复", b.String(), true)
		}
	}

	// E1 目录数量异常下跌（>20%）
	prevTotal := s.Cfg.NotifyPrevTotal
	if prevTotal > 100 && total > 0 && total < prevTotal*4/5 {
		s.notifyEvent("catalog_drop", "目录数量异常下跌",
			fmt.Sprintf("目录条目 %d → %d（跌幅 %d%%），上游源数据可能异常",
				prevTotal, total, (prevTotal-total)*100/prevTotal), false)
	}

	// 更新轮次状态
	notifyMu.Lock()
	s.Cfg.NotifyPrevFailed = make(map[string]bool, len(failed))
	for _, f := range failed {
		s.Cfg.NotifyPrevFailed[f.name] = true
	}
	s.Cfg.NotifyPrevTotal = total
	_ = s.Cfg.Save(dataDirOf(s))
	notifyMu.Unlock()

	// B 收藏应用有更新（目录已刷新，同步构建一次拿版本信息）
	s.favoriteUpdatePass()
	// 0.6.133 源同步摘要（各源应用数 / 应用总数含重复 / 关注源 / 收藏数目，指纹去重）
	s.sourceSummaryPass(sts, total)
	// 0.6.143 关注源新增应用（源×应用只推一次）
	s.favoriteSourcePass()
	// 0.6.190 关注源报表（总数 + 全清单 + 本轮新增/移除，有变化才推）
	s.favoriteSourceReportPass()
	// 0.6.133 应用更新摘要（已装应用更新集合变化才推）
	s.updatesAvailablePass()
}

// buildSourceSummary（0.6.143）：构造源同步摘要两个版本。
// compact = 外部渠道发（概况 + 应用数 Top5 + 失败源前 5，不刷屏）；
// full = 应用内通知记录（完整逐源明细 + 总数，方格折叠卡展开看）。
// 文案：逐源「N 个应用」；总数 = 各源原始条目数之和（含多源重复，不去重）。
func buildSourceSummary(sts []source.SourceStatus, total, favN, favSrcN int) (compact, full string) {
	okN, failedN := 0, 0
	for _, st := range sts {
		if st.Error == "" {
			okN++
		} else {
			failedN++
		}
	}

	// 完整版（应用内记录，折叠卡展开）：逐源明细 + 总数
	var fb strings.Builder
	fb.WriteString("本轮源同步：\n\n")
	for _, st := range sts {
		if st.Error != "" {
			fmt.Fprintf(&fb, "- %s：同步失败\n", st.Name)
		} else {
			fmt.Fprintf(&fb, "- %s：%d 个应用\n", st.Name, st.Count)
		}
	}
	fmt.Fprintf(&fb, "应用总数 %d（含多源重复）· 关注源 %d · 收藏 %d 个\n", total, favSrcN, favN)

	// 紧凑版（外部渠道）
	var b strings.Builder
	fmt.Fprintf(&b, "源同步：%d 源成功 / %d 源失败\n\n", okN, failedN)
	type row struct {
		name  string
		count int
	}
	var rows []row
	var failed []string
	for _, st := range sts {
		if st.Error != "" {
			failed = append(failed, st.Name)
			continue
		}
		rows = append(rows, row{st.Name, st.Count})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].count > rows[j].count })
	top := make([]string, 0, 5)
	for i, r := range rows {
		if i >= 5 {
			break
		}
		top = append(top, fmt.Sprintf("%s：%d", r.name, r.count))
	}
	fmt.Fprintf(&b, "应用总数 %d（含多源重复）\n", total)
	fmt.Fprintf(&b, "关注源 %d · 收藏 %d 个\n\n", favSrcN, favN)
	if len(top) > 0 {
		// 0.6.144 排版：Top5 用列表（企微 classic markdown 不支持段落空行，
		// 长 "/ " 拼接行换行断裂难读；列表每行独立，观感整齐）
		if len(rows) > 5 {
			fmt.Fprintf(&b, "应用数最多的源（等 %d 个源）：\n", len(rows))
		} else {
			b.WriteString("应用数最多的源：\n")
		}
		for _, t := range top {
			b.WriteString("- " + t + "\n")
		}
	}
	if len(failed) > 0 {
		sort.Strings(failed)
		names := make([]string, 0, len(failed))
		for _, n := range failed {
			if len(names) >= 5 {
				break
			}
			names = append(names, n)
		}
		b.WriteString("失败源：")
		b.WriteString(strings.Join(names, "、"))
		if len(failed) > 5 {
			fmt.Fprintf(&b, " 等 %d 个", len(failed))
		}
	}
	return strings.TrimSpace(b.String()), strings.TrimSpace(fb.String())
}

// sourceSummaryPass 源同步摘要（0.6.133 定稿；0.6.143 改版；0.6.144 加表格版）：
// 每轮源同步后聚合「各源应用数 / 应用总数（含多源重复，不去重）/ 关注源 / 收藏数目」。
// 外部渠道发紧凑版（markdown_v2 渠道发表格版）；应用内记录存完整逐源明细；
// 指纹不变不重复推。
func (s *Server) sourceSummaryPass(sts []source.SourceStatus, total int) {
	if !s.IsNotifyEventOn("source_sync_summary") {
		return
	}
	favN := len(s.Cfg.Favorites)
	favSrcN := len(s.Cfg.FavoriteSources)
	compact, fullC := buildSourceSummary(sts, total, favN, favSrcN)
	notifyMu.Lock()
	changed := s.Cfg.NotifyPrevSummaryFP != fullC
	if changed {
		s.Cfg.NotifyPrevSummaryFP = fullC
		_ = s.Cfg.Save(dataDirOf(s))
	}
	notifyMu.Unlock()
	if !changed {
		return
	}
	msg := newNotifyMsg("source_sync_summary", "源同步摘要", compact, true)
	msg.Full = fullC
	msg.Table, msg.Rows = buildSourceSummaryTable(sts, total, favN, favSrcN)
	msg.EmphTitle = fmt.Sprintf("%d", total)
	msg.EmphDesc = "应用总数（含多源重复）"
	// 0.6.175 通知信息长度变体：简洁=只发两个总数；完整=全量逐源不截断。
	// 0.6.178 简洁卡去重：大字强调已承载应用总数 → 两列行去掉「应用」行
	//（此前同一总数在 副题/行/大字 三处重复）；副题改源成功/失败数
	//（简洁卡中唯一不重复的信息）。
	msg.ContentConcise = fmt.Sprintf("应用源 %d 个 | 应用 %d 个", len(sts), total)
	okN, failedN := 0, 0
	for _, st := range sts {
		if st.Error == "" {
			okN++
		} else {
			failedN++
		}
	}
	msg.RowsConcise = []notify.CardRow{
		{Key: "应用源", Value: fmt.Sprintf("%d 个", len(sts))},
	}
	msg.ConciseSub = fmt.Sprintf("%d 源成功 / %d 源失败", okN, failedN)
	msg.ContentFull = fullC
	msg.TableFull, msg.RowsFull = buildSourceSummaryTableFull(sts, total, favN, favSrcN)
	writeNotifyLog(s.Cfg, msg, s.fanoutIfExternalOn(msg, "source_sync_summary"))
}

// buildSourceSummaryTableFull 完整详细模式（0.6.175）的 markdown_v2 表格 +
// 卡片行：全部源逐行列出，无 Top5 截断；卡片行上限 8 行（企微卡片展示
// 上限，超出部分应用内通知记录/详情页仍完整可查）。
func buildSourceSummaryTableFull(sts []source.SourceStatus, total, favN, favSrcN int) (string, []notify.CardRow) {
	type row struct {
		name  string
		count int
	}
	var rows []row
	okN := 0
	for _, st := range sts {
		if st.Error != "" {
			continue
		}
		okN++
		rows = append(rows, row{st.Name, st.Count})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].count > rows[j].count })
	var b strings.Builder
	fmt.Fprintf(&b, "源同步：%d 源成功 / %d 源失败\n\n| 源 | 应用数 |\n| :-- | --: |\n", okN, len(sts)-okN)
	cardRows := make([]notify.CardRow, 0, 8)
	for i, r := range rows {
		fmt.Fprintf(&b, "| %s | %d |\n", mdCell(r.name), r.count)
		if i < 8 {
			cardRows = append(cardRows, notify.CardRow{Key: r.name, Value: fmt.Sprintf("%d 个应用", r.count)})
		}
	}
	fmt.Fprintf(&b, "\n应用总数 %d（含多源重复）· 关注源 %d · 收藏 %d 个", total, favSrcN, favN)
	return b.String(), cardRows
}

// buildSourceSummaryTable 源同步摘要的 markdown_v2 表格版（0.6.144）：
// Top5 源 × 应用数 + 总量行。同时返回卡片两列列表行（Top5）。
func buildSourceSummaryTable(sts []source.SourceStatus, total, favN, favSrcN int) (string, []notify.CardRow) {
	type row struct {
		name  string
		count int
	}
	var rows []row
	okN := 0
	for _, st := range sts {
		if st.Error != "" {
			continue
		}
		okN++
		rows = append(rows, row{st.Name, st.Count})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].count > rows[j].count })
	var b strings.Builder
	fmt.Fprintf(&b, "源同步：%d 源成功 / %d 源失败\n\n| 源 | 应用数 |\n| :-- | --: |\n", okN, len(sts)-okN)
	cardRows := make([]notify.CardRow, 0, 5)
	for i, r := range rows {
		if i >= 5 {
			fmt.Fprintf(&b, "| … | 等 %d 源 |\n", len(rows))
			break
		}
		fmt.Fprintf(&b, "| %s | %d |\n", mdCell(r.name), r.count)
		cardRows = append(cardRows, notify.CardRow{Key: r.name, Value: fmt.Sprintf("%d 个应用", r.count)})
	}
	fmt.Fprintf(&b, "\n应用总数 %d（含多源重复）· 关注源 %d · 收藏 %d 个", total, favSrcN, favN)
	if len(rows) > 5 {
		fmt.Fprintf(&b, "（共 %d 源）", len(rows))
	}
	return b.String(), cardRows
}

// favoriteSourcePass 关注源新增应用通知（0.6.143）：每个关注源比对当前目录
// 应用集与已见基线，只推新增（源×应用只推一次）；首次关注记基线不推。
func (s *Server) favoriteSourcePass() {
	if len(s.Cfg.FavoriteSources) == 0 {
		return
	}
	catalog := s.cachedCatalog()
	bySrc := make(map[string][]AppInfo)
	for _, a := range catalog {
		if a.Source != "" {
			bySrc[a.Source] = append(bySrc[a.Source], a)
		}
	}
	type hit struct {
		src   string
		added []AppInfo
	}
	var hits []hit
	notifyMu.Lock()
	if s.Cfg.NotifyFavSourceSeen == nil {
		s.Cfg.NotifyFavSourceSeen = make(map[string]map[string]bool)
	}
	seen := s.Cfg.NotifyFavSourceSeen
	saved := false
	valid := make(map[string]bool, len(s.Cfg.Sources)+1)
	for _, sr := range s.Cfg.Sources {
		valid[sr.Name] = true
	}
	valid[OfficialSourceID] = true
	for name := range seen {
		if !valid[name] {
			delete(seen, name)
			saved = true
		}
	}
	for _, name := range s.Cfg.FavoriteSources {
		apps := bySrc[name]
		cur := make(map[string]bool, len(apps))
		for _, a := range apps {
			cur[a.Key] = true
		}
		base, known := seen[name]
		if !known {
			seen[name] = cur // 首次关注记基线，不推
			saved = true
			continue
		}
		var added []AppInfo
		for _, a := range apps {
			if !base[a.Key] {
				added = append(added, a)
				base[a.Key] = true
			}
		}
		if len(added) > 0 {
			saved = true
			// 防 config 膨胀：已见集合过大时重置为当前集
			if len(base) > 2000 {
				for k := range base {
					delete(base, k)
				}
				for k := range cur {
					base[k] = true
				}
			}
			hits = append(hits, hit{name, added})
		}
	}
	s.Cfg.NotifyFavSourceSeen = seen
	if saved {
		_ = s.Cfg.Save(dataDirOf(s))
	}
	notifyMu.Unlock()
	if len(hits) == 0 {
		return
	}
	// 0.6.194：每个关注源单独一张卡（用户要求：多关注源不挤一张卡）
	for _, h := range hits {
		s.pushFavSourceApps(h.src, h.added)
	}
}

// pushFavSourceApps 推送单个关注源的「新增应用」通知（0.6.194：每源一卡，
// md / md_v2 / 卡片行 均按单源构造）。
func (s *Server) pushFavSourceApps(name string, added []AppInfo) {
	n := len(added)
	appLine := func(a AppInfo) string {
		l, v := favAppLabelVer(a)
		if v != "" {
			return fmt.Sprintf("- %s（%s）", l, v)
		}
		return fmt.Sprintf("- %s", l)
	}
	// 友好 markdown
	var b strings.Builder
	fmt.Fprintf(&b, "关注源「%s」新增 %d 个应用：\n\n", name, n)
	for i, a := range added {
		if i >= 8 {
			fmt.Fprintf(&b, "- …等 %d 个\n", n)
			break
		}
		b.WriteString(appLine(a) + "\n")
	}
	// md_v2 表格
	var tb strings.Builder
	fmt.Fprintf(&tb, "关注源「%s」新增 %d 个应用：\n\n| 应用 | 版本 |\n| :-- | :-- |\n", name, n)
	for i, a := range added {
		if i >= 8 {
			fmt.Fprintf(&tb, "| … | 等 %d 个 |\n", n)
			break
		}
		l, v := favAppLabelVer(a)
		fmt.Fprintf(&tb, "| %s | %s |\n", mdCell(l), mdCell(orDash(v)))
	}
	// 卡片行
	rows := make([]notify.CardRow, 0, n)
	for i, a := range added {
		if i >= 6 {
			break
		}
		l, v := favAppLabelVer(a)
		rows = append(rows, notify.CardRow{Key: l, Value: orDash(v)})
	}
	// 完整（不截断）
	var bFull, tbFull strings.Builder
	fmt.Fprintf(&bFull, "关注源「%s」新增 %d 个应用：\n\n", name, n)
	fmt.Fprintf(&tbFull, "关注源「%s」新增 %d 个应用：\n\n| 应用 | 版本 |\n| :-- | :-- |\n", name, n)
	for _, a := range added {
		bFull.WriteString(appLine(a) + "\n")
		l, v := favAppLabelVer(a)
		fmt.Fprintf(&tbFull, "| %s | %s |\n", mdCell(l), mdCell(orDash(v)))
	}
	rowsFull := make([]notify.CardRow, 0, n)
	for _, a := range added {
		l, v := favAppLabelVer(a)
		rowsFull = append(rowsFull, notify.CardRow{Key: l, Value: orDash(v)})
	}
	if len(rowsFull) > 8 {
		rowsFull = rowsFull[:8]
	}
	s.notifyEventRichV("favorite_source_apps", "关注源新增 · "+name, strings.TrimSpace(b.String()), tb.String(), rows, "", "", true,
		&notify.Variants{
			ContentConcise: fmt.Sprintf("关注源「%s」新增 %d 个应用", name, n),
			ContentFull:    strings.TrimSpace(bFull.String()),
			TableFull:      tbFull.String(),
			RowsFull:       rowsFull,
		})
}

// favAppLabelVer 应用展示名 + 版本（关注源新增应用的通知文案共用）。
func favAppLabelVer(a AppInfo) (label, ver string) {
	label = a.DisplayName
	if label == "" {
		label = a.AppName
	}
	ver = a.AvailableVersion
	if ver == "" {
		ver = a.LatestVersion
	}
	return label, ver
}

// favoriteSourceReportPass 关注源报表（0.6.190；0.6.194 改每源一卡）：
// 每个关注源把当前目录应用集与上一轮快照（NotifyFavSourcePrev）比对——
// 应用集有变化（新增/移除）才推：当前应用总数 + 本轮新增/移除 + 全部应用
// 清单（含源地址）。**每个关注源单独一条通知**（用户要求：多源不挤一张卡，
// md / md_v2 / 卡片行均按单源构造）。首次关注记基线不推（与
// favorite_source_apps 同款语义）；无变化不推。
// 与 favorite_source_apps 独立开关（用户 2026-09-28 需求：「应用源有多少个
// 应用、有哪些应用、新增加了哪些应用」）。
func (s *Server) favoriteSourceReportPass() {
	if len(s.Cfg.FavoriteSources) == 0 {
		return
	}
	catalog := s.cachedCatalog()
	bySrc := make(map[string][]AppInfo)
	for _, a := range catalog {
		if a.Source != "" {
			bySrc[a.Source] = append(bySrc[a.Source], a)
		}
	}
	urlOf := make(map[string]string, len(s.Cfg.Sources))
	for _, sr := range s.Cfg.Sources {
		urlOf[sr.Name] = sr.URL
	}

	var reps []favSrcRep

	notifyMu.Lock()
	if s.Cfg.NotifyFavSourcePrev == nil {
		s.Cfg.NotifyFavSourcePrev = make(map[string]map[string]string)
	}
	prev := s.Cfg.NotifyFavSourcePrev
	saved := false
	valid := make(map[string]bool, len(s.Cfg.Sources)+1)
	for _, sr := range s.Cfg.Sources {
		valid[sr.Name] = true
	}
	valid[OfficialSourceID] = true
	for name := range prev {
		if !valid[name] {
			delete(prev, name)
			saved = true
		}
	}
	for _, name := range s.Cfg.FavoriteSources {
		apps := bySrc[name]
		// 展示名排序（报表清单稳定可读）
		sort.Slice(apps, func(i, j int) bool {
			li, _ := favAppLabelVer(apps[i])
			lj, _ := favAppLabelVer(apps[j])
			if li != lj {
				return li < lj
			}
			return apps[i].AppName < apps[j].AppName
		})
		curMap := make(map[string]string, len(apps))
		for _, a := range apps {
			l, v := favAppLabelVer(a)
			curMap[a.Key] = l
			if v != "" {
				curMap[a.Key] = l + "（" + v + "）"
			}
		}
		old, known := prev[name]
		if !known {
			prev[name] = curMap // 首次关注记基线，不推
			saved = true
			continue
		}
		var added []AppInfo
		for _, a := range apps {
			if _, was := old[a.Key]; !was {
				added = append(added, a)
			}
		}
		var removed []string
		for k, disp := range old {
			if _, now := curMap[k]; !now {
				removed = append(removed, disp)
			}
		}
		sort.Strings(removed)
		if len(added) == 0 && len(removed) == 0 {
			continue // 本轮无变化 → 不推
		}
		prev[name] = curMap
		saved = true
		reps = append(reps, favSrcRep{name: name, url: urlOf[name], cur: apps, added: added, removed: removed})
	}
	s.Cfg.NotifyFavSourcePrev = prev
	if saved {
		_ = s.Cfg.Save(dataDirOf(s))
	}
	notifyMu.Unlock()

	if len(reps) == 0 {
		return
	}
	// 0.6.194：每个关注源单独一张卡（用户要求：多关注源不挤一张卡，
	// md / md_v2 / 卡片行 均按单源构造）。
	for _, r := range reps {
		s.pushFavSourceReport(r)
	}
}

// favSrcRep 单个关注源的报表数据（0.6.194）。
type favSrcRep struct {
	name    string
	url     string
	cur     []AppInfo // 当前全清单（按展示名排序）
	added   []AppInfo
	removed []string // 被移除项（上一轮展示名「名（版本）」）
}

// splitDispVer 「名（版本）」拆回 名/版本（移除项展示用）。
func splitDispVer(disp string) (label, ver string) {
	if i := strings.LastIndex(disp, " ("); i > 0 && strings.HasSuffix(disp, ")") {
		return disp[:i], disp[i+2 : len(disp)-1]
	}
	return disp, ""
}

// pushFavSourceReport 推送单个关注源的报表（0.6.194）：
// 友好 md = 加粗标题 + 总数/增减 + 新增/移除/全部清单分节 + 源地址独立行
//（长 URL 不再挤在标题行里换行）；md_v2 = 单表「应用|版本|状态」（新增/移除
// 置顶）；卡片 = 概览行 + 新增应用行（≤6 行，企微卡片上限）。
func (s *Server) pushFavSourceReport(r favSrcRep) {
	addN, remN := len(r.added), len(r.removed)
	delta := ""
	switch {
	case addN > 0 && remN > 0:
		delta = fmt.Sprintf("（+%d 新增 / -%d 移除）", addN, remN)
	case addN > 0:
		delta = fmt.Sprintf("（+%d 新增）", addN)
	default:
		delta = fmt.Sprintf("（-%d 移除）", remN)
	}
	summary := fmt.Sprintf("当前 %d 个应用%s", len(r.cur), delta)

	appLine := func(a AppInfo) string {
		l, v := favAppLabelVer(a)
		if v != "" {
			return fmt.Sprintf("- %s（%s）", l, v)
		}
		return fmt.Sprintf("- %s", l)
	}

	// ---------- 友好 markdown ----------
	buildMD := func(full bool) string {
		var b strings.Builder
		fmt.Fprintf(&b, "**关注源「%s」**\n%s\n", r.name, summary)
		if addN > 0 {
			b.WriteString("\n**新增**\n")
			for i, a := range r.added {
				if !full && i >= 8 {
					fmt.Fprintf(&b, "- …等 %d 个\n", addN)
					break
				}
				b.WriteString(appLine(a) + "\n")
			}
		}
		if remN > 0 {
			b.WriteString("\n**移除**\n")
			for i, d := range r.removed {
				if !full && i >= 8 {
					fmt.Fprintf(&b, "- …等 %d 个\n", remN)
					break
				}
				fmt.Fprintf(&b, "- %s\n", d)
			}
		}
		if len(r.cur) > 0 {
			b.WriteString("\n**全部应用**\n")
			for i, a := range r.cur {
				if !full && i >= 8 {
					fmt.Fprintf(&b, "- …等 %d 个\n", len(r.cur))
					break
				}
				b.WriteString(appLine(a) + "\n")
			}
		}
		if r.url != "" {
			fmt.Fprintf(&b, "\n源地址：%s", r.url)
		}
		return strings.TrimRight(b.String(), "\n")
	}

	// ---------- markdown_v2 单表（应用|版本|状态，新增/移除置顶）----------
	buildTable := func(full bool) string {
		type row struct{ l, v, st string }
		var rows []row
		for _, a := range r.added {
			l, v := favAppLabelVer(a)
			rows = append(rows, row{l, v, "新增"})
		}
		for _, d := range r.removed {
			l, v := splitDispVer(d)
			rows = append(rows, row{l, v, "移除"})
		}
		for _, a := range r.cur {
			l, v := favAppLabelVer(a)
			rows = append(rows, row{l, v, ""})
		}
		var t strings.Builder
		fmt.Fprintf(&t, "**关注源「%s」** %s\n\n", r.name, summary)
		t.WriteString("| 应用 | 版本 | 状态 |\n| :-- | :-- | :-- |\n")
		limit := len(rows)
		if !full && limit > 8 {
			limit = 8
		}
		for _, rw := range rows[:limit] {
			fmt.Fprintf(&t, "| %s | %s | %s |\n", mdCell(rw.l), mdCell(orDash(rw.v)), rw.st)
		}
		if !full && limit < len(rows) {
			fmt.Fprintf(&t, "| … | | 等 %d 个 |\n", len(rows)-limit)
		}
		if r.url != "" {
			fmt.Fprintf(&t, "\n源地址：%s", r.url)
		}
		return strings.TrimRight(t.String(), "\n")
	}

	// ---------- 卡片行（概览 + 新增应用，≤6 行）----------
	rows := []notify.CardRow{{Key: "当前应用", Value: fmt.Sprintf("%d 个%s", len(r.cur), delta)}}
	for _, a := range r.added {
		if len(rows) >= 6 {
			break
		}
		l, v := favAppLabelVer(a)
		rows = append(rows, notify.CardRow{Key: l, Value: orDash(v)})
	}
	rowsFull := []notify.CardRow{{Key: "当前应用", Value: fmt.Sprintf("%d 个%s", len(r.cur), delta)}}
	for _, a := range r.added {
		l, v := favAppLabelVer(a)
		rowsFull = append(rowsFull, notify.CardRow{Key: l, Value: orDash(v)})
	}
	if len(rowsFull) > 8 {
		rowsFull = rowsFull[:8]
	}

	s.notifyEventRichV("favorite_source_report", "关注源报表 · "+r.name,
		buildMD(false), buildTable(false), rows, "", "", true,
		&notify.Variants{
			ContentConcise: fmt.Sprintf("关注源「%s」%s", r.name, summary),
			ContentFull:    buildMD(true),
			TableFull:      buildTable(true),
			RowsFull:       rowsFull,
		})
}


// updatesAvailablePass 应用更新摘要（0.6.133 用户定稿）：目录比对后收集
// 「已装且有更新」的应用集合；集合（应用×版本指纹）变化且非空才推送——
// 数目 + 每个应用的 旧→新 版本与来源（前 8 个，超出汇总）。
func (s *Server) updatesAvailablePass() {
	catalog := s.cachedCatalog()
	type upd struct{ label, old, new, src string }
	var upds []upd
	fps := make([]string, 0)
	for _, a := range catalog {
		if !a.Installed || !a.HasUpdate {
			continue
		}
		label := a.DisplayName
		if label == "" {
			label = a.AppName
		}
		src, _, _ := s.resolveKey(a.Key)
		upds = append(upds, upd{label, a.InstalledVersion, a.AvailableVersion, src})
		fps = append(fps, a.Key+"@"+a.AvailableVersion)
	}
	sort.Strings(fps)
	fp := strings.Join(fps, ",")
	notifyMu.Lock()
	changed := s.Cfg.NotifyPrevUpdatesFP != fp
	if changed {
		s.Cfg.NotifyPrevUpdatesFP = fp
		_ = s.Cfg.Save(dataDirOf(s))
	}
	notifyMu.Unlock()
	if !changed || len(upds) == 0 {
		return
	}
	sort.Slice(upds, func(i, j int) bool { return upds[i].label < upds[j].label })
	var b strings.Builder
	fmt.Fprintf(&b, "%d 个已装应用有新版本：\n\n", len(upds))
	for i, u := range upds {
		if i >= 8 {
			fmt.Fprintf(&b, "…等 %d 个\n", len(upds))
			break
		}
		if u.src != "" {
			fmt.Fprintf(&b, "- %s：%s → %s（%s）\n", u.label, orDash(u.old), u.new, u.src)
		} else {
			fmt.Fprintf(&b, "- %s：%s → %s\n", u.label, orDash(u.old), u.new)
		}
	}
	// 0.6.144：markdown_v2 表格版（应用 | 当前 | 最新）
	var tb strings.Builder
	fmt.Fprintf(&tb, "%d 个已装应用有新版本：\n\n| 应用 | 当前 | 最新 |\n| :-- | :-- | :-- |\n", len(upds))
	for i, u := range upds {
		if i >= 8 {
			fmt.Fprintf(&tb, "| … | 等 %d 个 |\n", len(upds))
			break
		}
		fmt.Fprintf(&tb, "| %s | %s | %s |\n", mdCell(u.label), mdCell(u.old), mdCell(u.new))
	}
	// 卡片两列列表（应用 | 旧 → 新，最多 6 行）
	rows := make([]notify.CardRow, 0, len(upds))
	for i, u := range upds {
		if i >= 6 {
			break
		}
		rows = append(rows, notify.CardRow{Key: u.label, Value: orDash(u.old) + " → " + u.new})
	}
	// 0.6.175 通知信息长度变体：简洁=只报数量；完整=全部更新条目不截断。
	var bFull, tbFull strings.Builder
	fmt.Fprintf(&bFull, "%d 个已安装应用有新版本：\n\n", len(upds))
	fmt.Fprintf(&tbFull, "%d 个已安装应用有新版本：\n\n| 应用 | 当前 | 最新 |\n| :-- | :-- | :-- |\n", len(upds))
	var rowsFull []notify.CardRow
	for i, u := range upds {
		if u.src != "" {
			fmt.Fprintf(&bFull, "- %s：%s → %s（%s）\n", u.label, orDash(u.old), u.new, u.src)
		} else {
			fmt.Fprintf(&bFull, "- %s：%s → %s\n", u.label, orDash(u.old), u.new)
		}
		fmt.Fprintf(&tbFull, "| %s | %s | %s |\n", mdCell(u.label), mdCell(u.old), mdCell(u.new))
		if i < 8 {
			rowsFull = append(rowsFull, notify.CardRow{Key: u.label, Value: orDash(u.old) + " → " + u.new})
		}
	}
	s.notifyEventRichV("updates_available", "应用更新摘要", strings.TrimSpace(b.String()), tb.String(), rows, "", "", true,
		&notify.Variants{
			ContentConcise: fmt.Sprintf("%d 个已安装应用有新版本", len(upds)),
			ContentFull:    bFull.String(),
			TableFull:      tbFull.String(),
			RowsFull:       rowsFull,
		})
}

func orDash(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

// mdCell 表格单元格净化（0.6.144）：竖线会破表格结构，换行折成空格。
func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", "｜")
	s = strings.ReplaceAll(s, "\n", " ")
	if s == "" {
		return "-"
	}
	return s
}

// favoriteUpdatePass 收藏应用版本比对（按 app key × 版本去重，每版只推一次）。
func (s *Server) favoriteUpdatePass() {
	if len(s.Cfg.Favorites) == 0 {
		return
	}
	catalog := s.cachedCatalog()
	byKey := make(map[string]AppInfo, len(catalog))
	for _, a := range catalog {
		byKey[a.Key] = a
	}
	type hit struct{ label, old, new, src string }
	var hits []hit
	notifyMu.Lock()
	if s.Cfg.NotifyFavNotified == nil {
		s.Cfg.NotifyFavNotified = make(map[string]string)
	}
	notified := s.Cfg.NotifyFavNotified
	notifyMu.Unlock()
	for _, key := range s.Cfg.Favorites {
		// 0.6.209：收藏可能存的是旧规范卡 key（裸 appname，如 "Gitea"），官方
		// 条目出现后规范卡 key 变带 @源名 后缀（"Gitea@fnos-official"）——精确
		// 未命中会让收藏静默失效。回退按裸名（@ 前部分）匹配，已装卡优先、
		// 官方源次之。
		a, ok := byKey[key]
		if !ok {
			bare := key
			if i := strings.LastIndex(key, "@"); i > 0 {
				bare = key[:i]
			}
			var best *AppInfo
			for ci := range catalog {
				c := &catalog[ci]
				cbare := c.Key
				if i := strings.LastIndex(c.Key, "@"); i > 0 {
					cbare = c.Key[:i]
				}
				if !strings.EqualFold(cbare, bare) {
					continue
				}
				if best == nil {
					best = c
					continue
				}
				if c.Installed && !best.Installed {
					best = c
				} else if c.Installed && best.Installed &&
					c.Source == OfficialSourceID && best.Source != OfficialSourceID {
					best = c
				}
			}
			if best != nil {
				a = *best
				ok = true
			}
		}
		if !ok {
			continue
		}
		latest := a.AvailableVersion
		if latest == "" {
			latest = a.LatestVersion
		}
		if latest == "" {
			continue
		}
		// 0.6.209：已装收藏的更新判定与「有更新」徽章同权威信号（规范卡
		// HasUpdate + AvailableVersion）。官方源空窗期规范卡曾回落社区源、
		// 其版本号体系跳变（Gitea 1.27.3→28.0.0）被误判为更新、渠道收到
		// 「错配更新」推送；现仅当权威信号确认有真实更新才推。
		if a.Installed && !a.HasUpdate {
			continue
		}
		// 已装且比已装版本新；未装则与「已通知过的版本」比（首次收藏不推，只记基线）
		base := a.InstalledVersion
		if !a.Installed {
			base = notified[key]
			if base == "" {
				notified[key] = latest // 记基线，不推
				continue
			}
		}
		if base == "" || compareVersions(latest, base) <= 0 {
			continue
		}
		if notified[key] == latest {
			continue // 已推过这个版本
		}
		label := a.DisplayName
		if label == "" {
			label = a.AppName
		}
		src, _, _ := s.resolveKey(key)
		hits = append(hits, hit{label, base, latest, src})
		notified[key] = latest
	}
	notifyMu.Lock()
	s.Cfg.NotifyFavNotified = notified
	_ = s.Cfg.Save(dataDirOf(s))
	notifyMu.Unlock()
	if len(hits) == 0 {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d 个收藏应用有新版本：\n\n", len(hits))
	for i, h := range hits {
		if i >= 8 {
			fmt.Fprintf(&b, "…等 %d 个\n", len(hits))
			break
		}
		if h.src != "" {
			fmt.Fprintf(&b, "- %s：%s → %s（%s）\n", h.label, h.old, h.new, h.src)
		} else {
			fmt.Fprintf(&b, "- %s：%s → %s\n", h.label, h.old, h.new)
		}
	}
	// 0.6.144：markdown_v2 表格版（应用 | 当前 | 最新 | 源）
	var tb strings.Builder
	fmt.Fprintf(&tb, "%d 个收藏应用有新版本：\n\n| 应用 | 当前 | 最新 | 源 |\n| :-- | :-- | :-- | :-- |\n", len(hits))
	for i, h := range hits {
		if i >= 8 {
			fmt.Fprintf(&tb, "| … | 等 %d 个 |\n", len(hits))
			break
		}
		fmt.Fprintf(&tb, "| %s | %s | %s | %s |\n", mdCell(h.label), mdCell(h.old), mdCell(h.new), mdCell(h.src))
	}
	// 卡片两列列表（应用 | 旧 → 新，最多 6 行）
	rows := make([]notify.CardRow, 0, len(hits))
	for i, h := range hits {
		if i >= 6 {
			break
		}
		rows = append(rows, notify.CardRow{Key: h.label, Value: h.old + " → " + h.new})
	}
	// 0.6.175 通知信息长度变体：简洁=只报数量；完整=全部收藏更新不截断。
	var bFull, tbFull strings.Builder
	fmt.Fprintf(&bFull, "%d 个收藏应用有新版本：\n\n", len(hits))
	fmt.Fprintf(&tbFull, "%d 个收藏应用有新版本：\n\n| 应用 | 当前 | 最新 | 源 |\n| :-- | :-- | :-- | :-- |\n", len(hits))
	var rowsFull []notify.CardRow
	for i, h := range hits {
		if h.src != "" {
			fmt.Fprintf(&bFull, "- %s：%s → %s（%s）\n", h.label, h.old, h.new, h.src)
		} else {
			fmt.Fprintf(&bFull, "- %s：%s → %s\n", h.label, h.old, h.new)
		}
		fmt.Fprintf(&tbFull, "| %s | %s | %s | %s |\n", mdCell(h.label), mdCell(h.old), mdCell(h.new), mdCell(h.src))
		if i < 8 {
			rowsFull = append(rowsFull, notify.CardRow{Key: h.label, Value: h.old + " → " + h.new})
		}
	}
	s.notifyEventRichV("favorite_update", "收藏应用有更新", strings.TrimSpace(b.String()), tb.String(), rows, "", "", true,
		&notify.Variants{
			ContentConcise: fmt.Sprintf("%d 个收藏应用有新版本", len(hits)),
			ContentFull:    bFull.String(),
			TableFull:      tbFull.String(),
			RowsFull:       rowsFull,
		})
}

// ---------- 自监控（C3 资源 / C4 健康） ----------

type selfMonitorState struct {
	mu            sync.Mutex
	memAlertSince time.Time // 持续超阈值起点（零 = 未超）
	cpuAlertSince time.Time
	memNotifiedAt time.Time // 30min 冷却
	cpuNotifiedAt time.Time
	healthDown    bool
	diskAlerting  bool      // 0.6.133：磁盘告警中（>90%）
	diskNotifiedAt time.Time // 30min 冷却
}

var selfMonState = &selfMonitorState{}

// StartSelfMonitor 5 分钟自检：资源占用 + 后端健康（由 main 启动 goroutine）。
// fmtDurHuman 通知消息里的时长中文表述（避免 Go 默认 "5m0s" 格式）。
func fmtDurHuman(d time.Duration) string {
	m := int(d.Minutes())
	if m < 1 {
		return "不足 1 分钟"
	}
	if m < 60 {
		return fmt.Sprintf("%d 分钟", m)
	}
	return fmt.Sprintf("%d 小时 %d 分钟", m/60, m%60)
}

func (s *Server) StartSelfMonitor(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.selfMonitorResources()
			s.selfMonitorHealth()
			s.diskSpaceCheck() // 0.6.133：磁盘空间 >90% 告警 / 回落恢复
		}
	}
}

// diskSpaceCheck 0.6.133：数据目录所在磁盘使用率 >90% 告警（30 分钟冷却），
// 回落 85% 以下推送恢复。df 探测（无新依赖），2 秒超时。
func (s *Server) diskSpaceCheck() {
	dir := dataDirOf(s)
	if dir == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "df", "-P", dir).Output()
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return
	}
	fields := strings.Fields(lines[len(lines)-1])
	// df -P 末字段 = 使用率（如 87%）
	if len(fields) < 6 {
		return
	}
	pctStr := strings.TrimSuffix(fields[4], "%")
	pct, err := strconv.Atoi(pctStr)
	if err != nil {
		return
	}
	st := selfMonState
	st.mu.Lock()
	defer st.mu.Unlock()
	now := time.Now()
	if pct > 90 {
		if !st.diskAlerting {
			st.diskAlerting = true
			st.diskNotifiedAt = time.Time{}
		}
		if now.Sub(st.diskNotifiedAt) >= 30*time.Minute && s.IsNotifyEventOn("disk_space_alert") {
			st.diskNotifiedAt = now
			s.notifyEvent("disk_space_alert", "磁盘空间告警",
				fmt.Sprintf("数据目录 %s 所在磁盘使用率 %d%%（>90%%），空间不足会导致应用安装/更新失败", dir, pct), false)
		}
	} else if pct <= 85 {
		if st.diskAlerting {
			st.diskAlerting = false
			if s.IsNotifyEventOn("disk_space_alert") {
				s.notifyEvent("disk_space_alert", "磁盘空间恢复",
					fmt.Sprintf("磁盘使用率回落到 %d%%（目录 %s）", pct, dir), true)
			}
		}
	}
}

// selfMonitorResources C3：RSS 超阈值持续 5 分钟 → 告警（30min 冷却）；
// 回落到 80% 阈值以下 → 恢复通知。CPU 同机制（进程级 /proc/self/stat）。
func (s *Server) selfMonitorResources() {
	mb := readRSSMB()
	pct := readCPUPct()
	st := selfMonState
	st.mu.Lock()
	defer st.mu.Unlock()
	now := time.Now()
	if mb <= 0 {
		return
	}
	memThresh := orDefaultInt(s.Cfg.NotifyMemAlertMB, 256)
	cpuThresh := orDefaultInt(s.Cfg.NotifyCPUPctAlert, 80)

	// 内存
	if mb > memThresh {
		if st.memAlertSince.IsZero() {
			st.memAlertSince = now
		} else if now.Sub(st.memAlertSince) >= 5*time.Minute &&
			now.Sub(st.memNotifiedAt) >= 30*time.Minute && s.IsNotifyEventOn("resource_mem_alert") {
			st.memNotifiedAt = now
			s.notifyEvent("resource_mem_alert", "内存占用告警",
				fmt.Sprintf("moo-server 内存 %dMB 超过阈值 %dMB（已持续 %s）",
					mb, memThresh, fmtDurHuman(now.Sub(st.memAlertSince))), false)
		}
	} else if mb < memThresh*4/5 {
		if !st.memAlertSince.IsZero() && s.IsNotifyEventOn("resource_mem_alert") {
			s.notifyEvent("resource_mem_alert", "内存占用恢复",
				fmt.Sprintf("moo-server 内存回落到 %dMB（阈值 %dMB）", mb, memThresh), true)
		}
		st.memAlertSince = time.Time{}
	}

	// CPU
	if pct > cpuThresh {
		if st.cpuAlertSince.IsZero() {
			st.cpuAlertSince = now
		} else if now.Sub(st.cpuAlertSince) >= 5*time.Minute &&
			now.Sub(st.cpuNotifiedAt) >= 30*time.Minute && s.IsNotifyEventOn("resource_cpu_alert") {
			st.cpuNotifiedAt = now
			s.notifyEvent("resource_cpu_alert", "CPU 占用告警",
				fmt.Sprintf("moo-server CPU %d%% 超过阈值 %d%%（已持续 %s）",
					pct, cpuThresh, fmtDurHuman(now.Sub(st.cpuAlertSince))), false)
		}
	} else if pct < cpuThresh*4/5 {
		if !st.cpuAlertSince.IsZero() && s.IsNotifyEventOn("resource_cpu_alert") {
			s.notifyEvent("resource_cpu_alert", "CPU 占用恢复",
				fmt.Sprintf("moo-server CPU 回落到 %d%%（阈值 %d%%）", pct, cpuThresh), true)
		}
		st.cpuAlertSince = time.Time{}
	}
}

// selfMonitorHealth C4：appcenter daemon RPC 可达 + 配置可读。
// 失败 → health_error；由失败转成功 → health_recovered。
func (s *Server) selfMonitorHealth() {
	var problems []string
	if !daemonReachable() {
		problems = append(problems, "appcenter daemon RPC 不可达")
	}
	if _, err := loadConfigForHealth(dataDirOf(s)); err != nil {
		problems = append(problems, "config.json 不可读: "+err.Error())
	}
	st := selfMonState
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(problems) > 0 {
		if !st.healthDown {
			st.healthDown = true
			s.notifyEvent("health_error", "后端健康异常", strings.Join(problems, "；"), false)
		}
		return
	}
	if st.healthDown {
		st.healthDown = false
		s.notifyEvent("health_recovered", "后端健康恢复", "自检恢复正常（daemon RPC 可达、配置可读）", true)
	}
}

// loadConfigForHealth 独立读一次 config.json（验证可解析）。
func loadConfigForHealth(dir string) (interface{}, error) {
	b, err := os.ReadFile(dir + "/config.json")
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, errors.New("config.json 为空")
	}
	return string(b), nil
}

// readRSSMB 进程常驻内存（MB）。
func readRSSMB() int {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if kb, err := strconv.Atoi(fields[1]); err == nil {
					return kb / 1024
				}
			}
		}
	}
	return 0
}

// readCPUPct 进程累计 CPU 百分比（相对单核；5 分钟窗口内的平均占用）。
// 用两次 /proc/self/stat 采样做差——单次调用只能拿到进程累计值，
// 这里用「距上次采样的增量 / 时间差」近似。
var lastCPUTick = struct {
	sync.Mutex
	tick int64
	at   time.Time
}{}

func readCPUPct() int {
	b, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0
	}
	s := string(b)
	// stat 第 14/15 字段 = utime/stime（clock tick）；字段 2 是 comm（带括号，可能含空格）
	i := strings.LastIndex(s, ")")
	if i < 0 {
		return 0
	}
	fields := strings.Fields(s[i+2:])
	if len(fields) < 12 {
		return 0
	}
	ut, _ := strconv.ParseInt(fields[11], 10, 64)
	stime, _ := strconv.ParseInt(fields[12], 10, 64)
	tick := ut + stime
	hz := int64(100) // Linux USER_HZ（绝大多数发行版 = 100）
	lastCPUTick.Lock()
	defer lastCPUTick.Unlock()
	now := time.Now()
	if lastCPUTick.at.IsZero() || tick < lastCPUTick.tick {
		lastCPUTick.tick = tick
		lastCPUTick.at = now
		return 0
	}
	elapsed := now.Sub(lastCPUTick.at).Seconds()
	lastCPUTick.tick = tick
	lastCPUTick.at = now
	if elapsed <= 0 {
		return 0
	}
	used := float64(tick-lastCPUTick.tick) / float64(hz)
	pct := int(used / elapsed * 100)
	if pct > 100 {
		pct = 100
	}
	return pct
}

// mirrorsAllFailedState 全挂监测状态，按组隔离（0.6.188：gh/dk 双组）。
// 仅各自探测循环协程访问（每周期一次），mu 兜底防并发写。
var mirrorAllFailedState = struct {
	mu   sync.Mutex
	at   map[string]time.Time // 最近一次告警推送时间（30min 冷却）
	down map[string]bool      // 处于「全部不可用」状态（供恢复通知对偶）
}{at: map[string]time.Time{}, down: map[string]bool{}}

// mirrorsAllFailedCheck D3：gh 组全部镜像失败 → 网络层异常（30min 冷却）。
func (s *Server) mirrorsAllFailedCheck() {
	s.mirrorsAllFailedCheckOf("gh")
}

// mirrorsAllFailedCheckOf <group> 组全部加速源探测失败 → 网络层异常推送
// （30 分钟冷却）；此前全挂 → 本轮有可用 = 恢复对偶推送（0.6.188 起 gh/dk
// 双组各自独立监测，挂各自探测循环）。
func (s *Server) mirrorsAllFailedCheckOf(group string) {
	groupLabel := "GitHub"
	failKey, failLabel := "mirrors_all_failed", "GitHub 加速源全部不可用"
	recKey, recLabel := "mirrors_recovered", "GitHub 加速源恢复"
	if group == "dk" {
		groupLabel = "Docker"
		failKey, failLabel = "docker_mirrors_all_failed", "Docker 加速源全部不可用"
		recKey, recLabel = "docker_mirrors_recovered", "Docker 加速源恢复"
	}
	stats := s.Mirrors.gh()
	if group == "dk" {
		stats = s.Mirrors.dk()
	}
	// 0.6.189：本地回环源（dk 组的 kspeeder，127.0.0.1:5443）不计入「全挂」
	// 判定——本地缓存活着不代表网络层正常；若计入，装了 KSpeeder 的机器
	// 此事件永远无法触发（0.6.188 用户实测反馈后修正）。
	probed := stats
	if group == "dk" {
		probed = probed[:0:0]
		for _, st := range stats {
			if st.Key != "kspeeder" {
				probed = append(probed, st)
			}
		}
	}
	if len(probed) == 0 {
		return
	}
	allFail := true
	for _, st := range probed {
		if st.Status == "ok" {
			allFail = false
			break
		}
	}
	mirrorAllFailedState.mu.Lock()
	defer mirrorAllFailedState.mu.Unlock()
	if allFail {
		if !mirrorAllFailedState.down[group] {
			mirrorAllFailedState.down[group] = true
			mirrorAllFailedState.at[group] = time.Time{}
		}
		if time.Since(mirrorAllFailedState.at[group]) < 30*time.Minute {
			return
		}
		mirrorAllFailedState.at[group] = time.Now()
		failMsg := fmt.Sprintf("全部 %d 个%s加速源探测失败，镜像拉取/下载可能整体变慢或失败（网络层异常）",
			len(probed), groupLabel)
		if group == "dk" {
			failMsg = fmt.Sprintf("全部 %d 个 Docker 远程加速源探测失败（本地 KSpeeder 缓存不计入），镜像拉取可能整体变慢或失败（网络层或 Docker Hub 异常）",
				len(probed))
		}
		s.notifyEvent(failKey, failLabel, failMsg, false)
		return
	}
	// 恢复对偶：此前全部不可用 → 本轮有可用 = 恢复
	if mirrorAllFailedState.down[group] {
		mirrorAllFailedState.down[group] = false
		if best := bestKey(stats); best != "" && s.IsNotifyEventOn(recKey) {
			s.notifyEvent(recKey, recLabel,
				fmt.Sprintf("%s 加速源已恢复可用，当前优选：%s", groupLabel, best), true)
		}
	}
}
