package platform

// NormalizeProgress 归一 daemon 上报的任务进度为 0~1 比例。
//
// 实锤出处（两种量纲）：
//   - 0.6.320 installDownloadSSE 现场注释（store_routes.go）：daemon 安装任务
//     存在「2110 = 21.1%」量级的上报，即千分比（0.1% 单位，>100）；
//   - stageStatus/taskStatus.Progress 常规上报为百分数（0~100，daemon.go:573
//     「onProgress 回调进度 0~100」）。
//
// 规则（沿用 installDownloadSSE 既有实现，行为不变）：
//   - >100 视为千分比，先 /100 得百分数；
//   - 钳制上界 100（再大的脏值也只算 100%）；
//   - 转成 0~1 比例并钳制下界 0（负值脏数据归 0）。
func NormalizeProgress(frac float64) float64 {
	n := frac
	if n > 100 {
		n /= 100
	}
	if n > 100 {
		n = 100
	}
	n /= 100
	if n < 0 {
		n = 0
	}
	return n
}
