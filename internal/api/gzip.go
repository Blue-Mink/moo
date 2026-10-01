package api

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strings"
)

// gzipMiddleware 透明压缩响应：客户端声明 Accept-Encoding: gzip 时，
// 首次 Write 嗅探 Content-Type——json/text/html 走 gzip，二进制（图标等）
// 直通。SSE（text/event-stream）同样直通，且 Flusher 透传保证流式推送。
// 收益：/api/apps 列表 1.5MB→257KB（6 倍），fn connect 公网打开列表
// 从秒级降到百毫秒级。
func withGzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		wr := &gzSniffWriter{ResponseWriter: w, buf: &bytes.Buffer{}}
		next.ServeHTTP(wr, r)
		wr.finish() // gzip 尾部必须 Close 刷出（<4KB 内容不 Close 会整个丢）
	})
}

type gzSniffWriter struct {
	http.ResponseWriter
	buf      *bytes.Buffer // gzip 决策前的写入暂存
	gz       *gzip.Writer
	started  bool // 已开始 gzip
	passthru bool
	status   int
}

func (w *gzSniffWriter) decide() {
	if w.started || w.passthru {
		return
	}
	ct := w.Header().Get("Content-Type")
	// text/event-stream 必须直通：SSE 流式推送不能被压缩缓冲/编码，
	// 面板网关与 EventSource 客户端对 gzip SSE 的处理不可靠。
	compressible := strings.HasPrefix(ct, "application/json") ||
		(strings.HasPrefix(ct, "text/") && !strings.HasPrefix(ct, "text/event-stream")) ||
		strings.Contains(ct, "html")
	if !compressible {
		w.passthru = true
		// 暂存内容直通写出
		if w.status != 0 {
			w.ResponseWriter.WriteHeader(w.status)
		}
		if w.buf != nil && w.buf.Len() > 0 {
			_, _ = w.ResponseWriter.Write(w.buf.Bytes())
		}
		w.buf = nil
		return
	}
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Del("Content-Length")
	if w.status != 0 {
		w.ResponseWriter.WriteHeader(w.status)
	}
	gz, _ := gzip.NewWriterLevel(w.ResponseWriter, gzip.DefaultCompression)
	w.gz = gz
	w.started = true
	if w.buf != nil && w.buf.Len() > 0 {
		_, _ = gz.Write(w.buf.Bytes())
	}
	w.buf = nil
}

func (w *gzSniffWriter) WriteHeader(code int) {
	if w.started {
		return // 状态行已发
	}
	w.status = code
	// Write 未发生前无法嗅探——不立即决定，等首次 Write
	if w.buf == nil && w.passthru {
		w.ResponseWriter.WriteHeader(code)
	}
}

func (w *gzSniffWriter) Write(p []byte) (int, error) {
	w.decide()
	if w.started {
		return w.gz.Write(p)
	}
	if w.passthru {
		return w.ResponseWriter.Write(p)
	}
	// 未决策：暂存（正常路径首次 Write 即触发 decide，此分支仅兜底）
	n, _ := w.buf.Write(p)
	return n, nil
}

func (w *gzSniffWriter) Flush() {
	w.decide()
	if w.started {
		_ = w.gz.Flush()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Close 结束响应（handler 返回后由 withGzip 兜底调用）。
func (w *gzSniffWriter) finish() {
	w.decide()
	if w.started {
		_ = w.gz.Close()
	}
}

var _ io.Writer = (*gzSniffWriter)(nil)
var _ http.Flusher = (*gzSniffWriter)(nil)
