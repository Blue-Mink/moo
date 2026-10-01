package api

import (
	"net/http"
	"sort"

	"moo/internal/source"
)

// searchSourceKeys（0.6.198）公开端点：「归一化 key → 源名集合」。
// 搜索框贴源链接（仓库根 / raw 前缀 / JSON 索引 / fndepot v1v2 / 镜像形态）
// 时，前端用 sourceKey.ts 归一化词条后与此 map 匹配 → 源过滤，搜出该源
// 全部应用。公开（untrusted 直连上下文也可用）——返回的是源 URL 的归一化
// 形式（github.com/owner/repo 或 host+path），不含原始 URL；key 本身即
// 公开仓库标识，无敏感信息。
func (s *Server) searchSourceKeys(w http.ResponseWriter, r *http.Request) {
	type entry struct {
		Key   string   `json:"key"`
		Names []string `json:"names"`
	}
	m := make(map[string][]string)
	for _, sr := range s.Cfg.Sources {
		k := source.SourceURLKey(sr.URL)
		if k == "" {
			continue
		}
		names := append(m[k], sr.Name)
		// 内置官方目录的源列表名（fnos-store）与应用数据 source 值
		// （fnos-apps）不同名——两个都登记，保证链接能命中官方源应用。
		if sr.Name == "fnos-store" {
			names = append(names, "fnos-apps")
		}
		m[k] = names
	}
	out := make([]entry, 0, len(m))
	for k, names := range m {
		out = append(out, entry{k, names})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	writeJSON(w, out)
}
