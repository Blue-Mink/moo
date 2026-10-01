package api

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"math/rand"
	"testing"
)

// noisePNG 生成 w×h 随机噪声 PNG（不可压缩，编码体积 ≈ w*h*3/4，
// 保证 >32KB 以进入缩样路径；固定种子可复现）。
func noisePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewSource(42))
	pix := make([]byte, w*h*4)
	r.Read(pix)
	copy(img.Pix, pix)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func decodeDims(t *testing.T, b []byte) (int, int) {
	t.Helper()
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	return cfg.Width, cfg.Height
}

func TestDownsampleIcon_SmallUntouched(t *testing.T) {
	// 纯色 100x100（编码极小，<32KB 且尺寸达标）
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for i := range img.Pix {
		img.Pix[i] = 120
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	small := buf.Bytes()
	out, ct, changed := downsampleIcon(small, "image/png")
	if changed || !bytes.Equal(out, small) {
		t.Fatalf("小图应原样返回 (changed=%v)", changed)
	}
	if ct != "image/png" {
		t.Fatalf("ctype 应不变: %s", ct)
	}
}

func TestDownsampleIcon_UnderSizeThreshold(t *testing.T) {
	// 尺寸超 256 但编码极小（纯色 <32KB）→ 不处理
	img := image.NewRGBA(image.Rect(0, 0, 500, 500))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	in := buf.Bytes()
	if len(in) >= iconDownsampleMin {
		t.Fatalf("测试前提不成立: 纯色图应 <32KB, got %d", len(in))
	}
	out, _, changed := downsampleIcon(in, "image/png")
	if changed || !bytes.Equal(out, in) {
		t.Fatalf("<32KB 应原样返回 (changed=%v)", changed)
	}
}

func TestDownsampleIcon_ResizesLarge(t *testing.T) {
	in := noisePNG(t, 512, 512)
	if len(in) < iconDownsampleMin {
		t.Fatalf("测试前提不成立: 512x512 渐变应 >32KB, got %d", len(in))
	}
	out, ct, changed := downsampleIcon(in, "image/png")
	if !changed {
		t.Fatalf("大尺寸图应被处理 (in=%d bytes)", len(in))
	}
	if ct != "image/png" {
		t.Fatalf("成功后 ctype 应为 image/png: %s", ct)
	}
	w, h := decodeDims(t, out)
	if w > iconMaxDim || h > iconMaxDim {
		t.Fatalf("最长边应 ≤%d: %dx%d", iconMaxDim, w, h)
	}
	if len(out) >= len(in) {
		t.Fatalf("应更小的字节: in=%d out=%d", len(in), len(out))
	}
}

func TestDownsampleIcon_AspectKept(t *testing.T) {
	in := noisePNG(t, 800, 400)
	out, _, changed := downsampleIcon(in, "image/png")
	if !changed {
		t.Fatalf("应被处理")
	}
	w, h := decodeDims(t, out)
	if w != iconMaxDim || h != iconMaxDim/2 {
		t.Fatalf("应保持 2:1 比例: got %dx%d (期望 %dx%d)", w, h, iconMaxDim, iconMaxDim/2)
	}
}

func TestDownsampleIcon_JPEGToPNG(t *testing.T) {
	// JPEG 源 → 输出恒为 PNG
	in := noisePNG(t, 600, 300)
	img, _ := png.Decode(bytes.NewReader(in))
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
	jpegIn := buf.Bytes()
	if len(jpegIn) < iconDownsampleMin {
		t.Fatalf("测试前提不成立: got %d", len(jpegIn))
	}
	out, ct, changed := downsampleIcon(jpegIn, "image/jpeg")
	if !changed {
		t.Fatalf("JPEG 大图应被处理")
	}
	if ct != "image/png" {
		t.Fatalf("JPEG 源成功后应转 image/png: %s", ct)
	}
	w, h := decodeDims(t, out)
	if w > iconMaxDim || h > iconMaxDim {
		t.Fatalf("超限: %dx%d", w, h)
	}
}

func TestDownsampleIcon_NonImagePassthrough(t *testing.T) {
	// 非图片字节（如误判的 HTML）>32KB → 原样
	in := make([]byte, 40*1024)
	for i := range in {
		in[i] = byte('a' + i%26)
	}
	out, ct, changed := downsampleIcon(in, "text/html")
	if changed || !bytes.Equal(out, in) || ct != "text/html" {
		t.Fatalf("非图片应原样返回 (changed=%v)", changed)
	}
}

func TestDownsampleIcon_AlreadyCappedDims(t *testing.T) {
	// >32KB 但尺寸 ≤256（大编码小图）→ 原样（不重编码）
	in := noisePNG(t, 200, 200)
	if len(in) < iconDownsampleMin {
		t.Fatalf("测试前提不成立: got %d", len(in))
	}
	out, _, changed := downsampleIcon(in, "image/png")
	if changed || !bytes.Equal(out, in) {
		t.Fatalf("≤256px 应原样返回（不重编码）(changed=%v)", changed)
	}
}
