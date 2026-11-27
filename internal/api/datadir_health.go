package api

import (
	"net/http"

	"moo/internal/config"
)

// datadirHealth GET /api/datadir-health（0.6.574，复核 F1 收口）。
//
// 背景：`config.Save` 有 32 个调用点写成 `_ = s.cfg().Save(...)`，守卫拒绝或
// 写盘失败在运行期只留一行日志，界面照常 200 —— 「保存成功但没落盘」看不见。
// 这里把 `config.SaveStats()` 的计数、生效目录与归属标记一起暴露给管理员，
// 供排障与后续「设置页横幅 / 节流通知」接入。
//
// 只读、无副作用；返回计数自进程启动累计（不落盘，重启归零）。
func (s *Server) datadirHealth(w http.ResponseWriter, r *http.Request) {
	dir := config.DataDir()
	rejected, failed := config.SaveStats()
	resp := map[string]any{
		"data_dir":      dir,
		"data_dir_src":  datadirSourceLabel(),
		"app_name":      config.AppName(),
		"save_rejected": rejected,
		"save_failed":   failed,
		"ok":            rejected == 0 && failed == 0,
	}
	if v, err := config.ReadDataDirOwner(dir); err == nil {
		resp["owner"] = v
	} else {
		resp["owner_error"] = err.Error()
	}
	writeJSON(w, resp)
}

// datadirSourceLabel 与 server 启动日志同口径的数据目录来源。
func datadirSourceLabel() string {
	if config.MOODataExplicit() {
		return "MOO_DATA"
	}
	return "TRIM_PKGVAR/默认"
}
