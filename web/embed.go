// Package web 内嵌 Moo 前端构建产物（vite build → dist/）。
package web

import "embed"

//go:embed all:dist
// Dist 是前端构建产物。构建 FPK 前必须先跑 `npm run build` 生成 dist/。
var Dist embed.FS
