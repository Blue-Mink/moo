package api

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
)

// mergeOfficialCardsLegacy 0.6.312 B3/F7 等价性基线：buildCatalog 原内联
// 嵌套循环实现（O(官方×目录)）逐行保留，仅用于与 map 版对拍。
func mergeOfficialCardsLegacy(out []AppInfo, official []AppInfo, seen map[string]bool) []AppInfo {
	for _, oa := range official {
		idx := -1
		for i := range out {
			if out[i].AppName == oa.AppName {
				idx = i
				break
			}
		}
		if idx < 0 {
			if seen[oa.AppName] {
				continue
			}
			seen[oa.AppName] = true
			out = append(out, oa)
			continue
		}
		e := &out[idx]
		if e.Source == "" {
			e.Category = oa.Category
			e.AppType = oa.AppType
			e.DownloadCount = oa.DownloadCount
			if e.LatestVersion == "" {
				e.LatestVersion = oa.LatestVersion
			}
		}
		e.Source = OfficialSourceID
		if oa.DownloadCount != nil {
			e.DownloadCount = oa.DownloadCount
		}
		if e.Description == "" {
			e.Description = oa.Description
		}
		if oa.Maintainer != "" {
			e.Maintainer = oa.Maintainer
			if oa.MaintainerURL != "" {
				e.MaintainerURL = oa.MaintainerURL
			}
		}
		if oa.Distributor != "" {
			e.Distributor = oa.Distributor
			if oa.DistributorURL != "" {
				e.DistributorURL = oa.DistributorURL
			}
		}
		if e.Installed && oa.LatestVersion != "" {
			applyOfficialUpdateForInstalled(e, oa)
		} else if !e.Installed {
			if oa.LatestVersion != "" && compareVersions(oa.LatestVersion, e.LatestVersion) > 0 {
				e.LatestVersion = oa.LatestVersion
			}
		}
	}
	return out
}

// TestMergeOfficialCards_Equivalence F7：map 版官方合并与嵌套循环版在
// 200 组随机输入（同名卡/无源已装卡/官方重名条目/空描述）下逐字段等价，
// 含 seen 副作用。
func TestMergeOfficialCards_Equivalence(t *testing.T) {
	rng := rand.New(rand.NewSource(20261007))
	for trial := 0; trial < 200; trial++ {
		var out []AppInfo
		seen := map[string]bool{}
		for i := 0; i < rng.Intn(30); i++ {
			name := fmt.Sprintf("app%d", rng.Intn(12))
			ai := AppInfo{
				AppName:          name,
				Source:           fmt.Sprintf("src%d", rng.Intn(4)),
				Installed:        rng.Intn(3) == 0,
				LatestVersion:    fmt.Sprintf("1.%d.%d", rng.Intn(5), rng.Intn(5)),
				InstalledVersion: fmt.Sprintf("1.%d.0", rng.Intn(5)),
			}
			if rng.Intn(4) == 0 {
				ai.Source = "" // 无源卡（已装无源/官方回填场景）
			}
			if rng.Intn(3) == 0 {
				ai.Description = "x" // 非空描述（官方不覆盖）
			}
			out = append(out, ai)
			seen[name] = true
		}
		var official []AppInfo
		for i := 0; i < rng.Intn(10); i++ {
			name := fmt.Sprintf("app%d", rng.Intn(14)) // app12/13 = 官方独有（走追加分支，可重复）
			dc := rng.Intn(100)
			official = append(official, AppInfo{
				AppName:       name,
				Key:           name + "@" + OfficialSourceID,
				LatestVersion: fmt.Sprintf("1.%d.%d", rng.Intn(6), rng.Intn(6)),
				DownloadCount: &dc,
				Maintainer:    "maint",
				Distributor:   "dist",
				Description:   "official desc",
			})
		}
		base := func() []AppInfo {
			c := make([]AppInfo, len(out))
			copy(c, out)
			return c
		}
		seen1, seen2 := map[string]bool{}, map[string]bool{}
		for k := range seen {
			seen1[k] = true
			seen2[k] = true
		}
		got := mergeOfficialCards(base(), official, seen1)
		want := mergeOfficialCardsLegacy(base(), official, seen2)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("trial %d: map 版与嵌套循环版不一致\ngot:  %#v\nwant: %#v", trial, got, want)
		}
		if !reflect.DeepEqual(seen1, seen2) {
			t.Fatalf("trial %d: seen 副作用不一致: %v vs %v", trial, seen1, seen2)
		}
	}
}
