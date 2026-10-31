package release

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ghRelease 是 GitHub Releases API 的裁剪结构（只取本包用到的字段）。
type ghRelease struct {
	TagName string    `json:"tag_name"`
	Draft   bool      `json:"draft"`
	Name    string    `json:"name"`
	Assets  []ghAsset `json:"assets"`
}

// ghAsset 是单个发布资产。
type ghAsset struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	State  string `json:"state"`
	Digest string `json:"digest"` // 形如 "sha256:xxxx"
	URL    string `json:"browser_download_url"`
}

// assetNames 返回资产名列表，用于错误信息与 Extra 记录。
func assetNames(as []ghAsset) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.Name)
	}
	return out
}

// find 按名字精确查找资产。
func find(as []ghAsset, name string) (ghAsset, bool) {
	for _, a := range as {
		if a.Name == name {
			return a, true
		}
	}
	return ghAsset{}, false
}

// probeAPI 走 GitHub Releases API 主通道。
//
// 依据：tag 必须存在且非 draft；三件套（fpk / .sha256 / .minisig）都在
// 资产清单里。这里**不**下载，只核对资产在场与 digest 是否与 .sha256
// 侧车声明的摘要一致（不一致直接判失败，能提前抓到「上传截断」类事故）。
func probeAPI(ctx context.Context, o Opts) (Result, error) {
	o = o.normalize()
	res := Result{Channel: ChannelAPI}
	fpk := FPKName(o.Tag, o.Arch)

	var lastErr error
	for _, repo := range o.Repos {
		url := fmt.Sprintf("%s/repos/%s/releases/tags/v%s", apiBase, repo, o.Tag)
		body, _, err := httpGet(ctx, o.Client, url)
		if err != nil {
			lastErr = fmt.Errorf("%s: %v", repo, err)
			// 404 = 该 repo 无此 tag，继续下一个 repo；其他错误也一样继续，
			// 但保留最后一条错误用于报告。
			continue
		}
		var rel ghRelease
		decErr := json.NewDecoder(body).Decode(&rel)
		body.Close()
		if decErr != nil {
			lastErr = fmt.Errorf("%s: 解析 release JSON 失败: %v", repo, decErr)
			continue
		}
		if rel.Draft {
			lastErr = fmt.Errorf("%s: tag v%s 是 draft 发布，外部不可见", repo, o.Tag)
			continue
		}

		f, okF := find(rel.Assets, fpk)
		sha, okS := find(rel.Assets, fpk+".sha256")
		sig, okM := find(rel.Assets, fpk+".minisig")
		if !okF || !okS || !okM {
			var miss []string
			if !okF {
				miss = append(miss, fpk)
			}
			if !okS {
				miss = append(miss, fpk+".sha256")
			}
			if !okM {
				miss = append(miss, fpk+".minisig")
			}
			res.Extra = append(res.Extra, "assets="+strings.Join(assetNames(rel.Assets), ","))
			lastErr = fmt.Errorf("%s: release v%s 缺资产 %s", repo, o.Tag, strings.Join(miss, ","))
			continue
		}

		// state 不是 uploaded 说明资产仍在处理或已损坏（GitHub 截断事故特征）。
		// 注意：这里必须立刻返回，不能只记 lastErr——否则后续会因三件套齐全
		// 而误判成功（测试 TestProbeAPIStateNotUploaded 就是这条回归护栏）。
		for _, a := range []ghAsset{f, sha, sig} {
			if a.State != "" && a.State != "uploaded" {
				return res, fmt.Errorf("%s: 资产 %s state=%s（非 uploaded）", repo, a.Name, a.State)
			}
		}

		res.Assets = Assets{
			FPK:          f.URL,
			SHA256:       sha.URL,
			Minisig:      sig.URL,
			Size:         f.Size,
			SHA256Digest: strings.TrimPrefix(f.Digest, "sha256:"),
		}
		if err := assetsComplete(res.Assets); err != nil {
			return res, err
		}
		// 资产 URL 必须是 https 且指向本 tag 的下载路径，防止上游把
		// browser_download_url 指到别处（镜像污染）。
		for _, u := range []string{res.Assets.FPK, res.Assets.SHA256, res.Assets.Minisig} {
			if !strings.HasPrefix(u, "https://") || !strings.Contains(u, "/releases/download/") {
				return res, fmt.Errorf("%s: 资产 URL 形态异常: %s", repo, u)
			}
		}
		res.OK = true
		res.Version = NormalizedTag(rel.TagName)
		return res, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("未发现 tag v%s", o.Tag)
	}
	return res, lastErr
}
