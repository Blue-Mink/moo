package api

import (
	_ "embed"
	"encoding/json"
	"sync"
)

// 0.6.311r：构建时内嵌当前版本发布说明。changelog_current.json 由 build.sh
// 在 go build 前生成（单一事实源 = moo.json 顶层 version/changelog，版本号取自
// manifest）：版本不匹配或缺失时内嵌文本为空，卡片回落源条目，不阻塞构建。
//
// 用途：关于页「最新更新日志」卡取「源最新条目 vs 本内嵌」中版本号更大者——
// 运行构建比源新（版本尚未发布/源未同步）时仍能看到本构建自己的日志，
// 源比运行版本新时则预览最新版日志（卡恒随最新版本）。
//
//go:embed changelog_current.json
var changelogCurrentRaw []byte

type changelogCurrentFile struct {
	Version   string `json:"version"`
	Changelog string `json:"changelog"`
}

var (
	changelogOnce   sync.Once
	changelogParsed changelogCurrentFile
)

// changelogCurrentFor 返回与 version 匹配的内嵌发布说明；
// 文件缺失/解析失败/版本不匹配一律返回空串（调用方回落源条目）。
func changelogCurrentFor(version string) string {
	changelogOnce.Do(func() {
		_ = json.Unmarshal(changelogCurrentRaw, &changelogParsed)
	})
	if changelogParsed.Version != version {
		return ""
	}
	return changelogParsed.Changelog
}
