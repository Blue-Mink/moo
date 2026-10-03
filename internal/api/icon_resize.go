package api

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif" // 注册 GIF 解码器（image.Decode 分发用）
	_ "image/jpeg"
	"image/png"
)

// 0.6.201 图标降采样（借鉴 FnDepot 的 downsampling，纯标准库实现）：
// 社区/官方图标多为全尺寸原图（2026-09 实测 clientlink 783KB、
// ddnsto/we-mp-rss 265KB），图标占 fn connect 2Mbps 中继会话字节的
// 85–95%，是列表「图标一个一个蹦」延迟的最大头。
//
// 策略（保守，任何失败路径都不影响图标显示）：
//   - 只处理 > 32KB 的字节流（小图直接过，零开销）；
//   - 解码后最长边 > 256px 才缩（图标实际渲染 96–192px，256 足够 2x）；
//   - 双线性缩放到最长边 256px，重编码为 PNG；
//   - 重编码没变小（源已是紧凑编码）或解码失败（SVG/WebP/损坏）→
//     原样返回，绝不放大、绝不丢图。
const (
	iconMaxDim        = 256
	iconDownsampleMin = 32 * 1024
)

// downsampleIcon 返回降采样后的字节、content-type（成功时恒为 image/png）
// 与 changed（是否真正处理；未处理时原样返回 data/ctype）。
func downsampleIcon(data []byte, ctype string) ([]byte, string, bool) {
	if len(data) < iconDownsampleMin {
		return data, ctype, false
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return data, ctype, false
	}
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= iconMaxDim && sh <= iconMaxDim {
		return data, ctype, false // 尺寸已达标（编码再紧凑也没必要动）
	}
	if sw < 2 || sh < 2 {
		return data, ctype, false // 退化形状，不处理
	}
	// 统一成 RGBA 平面缓冲（避免循环内 src.At 接口派发；透明底一并归一）
	flat := image.NewRGBA(b)
	draw.Draw(flat, b, src, b.Min, draw.Src)

	var dw, dh int
	if sw >= sh {
		dw = iconMaxDim
		dh = sh * iconMaxDim / sw
	} else {
		dh = iconMaxDim
		dw = sw * iconMaxDim / sh
	}
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	bilinearDownscale(flat, sw, sh, dst, dw, dh)

	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return data, ctype, false
	}
	if buf.Len() >= len(data) {
		return data, ctype, false // 没缩到更小 → 保留原字节（编码更紧凑）
	}
	return buf.Bytes(), "image/png", true
}

// bilinearDownscale 双线性缩小（仅缩小路径；中心采样避免边缘半像素偏移）。
func bilinearDownscale(src *image.RGBA, sw, sh int, dst *image.RGBA, dw, dh int) {
	pitch := sw * 4
	for y := 0; y < dh; y++ {
		fy := (float64(y) + 0.5) * float64(sh) / float64(dh)
		y0 := int(fy)
		if y0 > sh-2 {
			y0 = sh - 2
		}
		wy := fy - float64(y0)
		for x := 0; x < dw; x++ {
			fx := (float64(x) + 0.5) * float64(sw) / float64(dw)
			x0 := int(fx)
			if x0 > sw-2 {
				x0 = sw - 2
			}
			wx := fx - float64(x0)

			i00 := y0*pitch + x0*4
			i10 := i00 + 4
			i01 := i00 + pitch
			i11 := i01 + 4

			r := float64(src.Pix[i00])*iwih(wx, wy) + float64(src.Pix[i10])*xwih(wx, wy) +
				float64(src.Pix[i01])*iwy(wx, wy) + float64(src.Pix[i11])*xwy(wx, wy)
			g := float64(src.Pix[i00+1])*iwih(wx, wy) + float64(src.Pix[i10+1])*xwih(wx, wy) +
				float64(src.Pix[i01+1])*iwy(wx, wy) + float64(src.Pix[i11+1])*xwy(wx, wy)
			bl := float64(src.Pix[i00+2])*iwih(wx, wy) + float64(src.Pix[i10+2])*xwih(wx, wy) +
				float64(src.Pix[i01+2])*iwy(wx, wy) + float64(src.Pix[i11+2])*xwy(wx, wy)
			a := float64(src.Pix[i00+3])*iwih(wx, wy) + float64(src.Pix[i10+3])*xwih(wx, wy) +
				float64(src.Pix[i01+3])*iwy(wx, wy) + float64(src.Pix[i11+3])*xwy(wx, wy)

			dst.SetRGBA(x, y, color.RGBA{
				R: clampByte(r), G: clampByte(g), B: clampByte(bl), A: clampByte(a),
			})
		}
	}
}

// 双线性四权重（内联函数省结构体分配）
func iwih(wx, wy float64) float64 { return (1 - wx) * (1 - wy) }
func xwih(wx, wy float64) float64 { return wx * (1 - wy) }
func iwy(wx, wy float64) float64  { return (1 - wx) * wy }
func xwy(wx, wy float64) float64  { return wx * wy }

func clampByte(f float64) uint8 {
	if f < 0 {
		return 0
	}
	if f > 255 {
		return 255
	}
	return uint8(f + 0.5)
}
