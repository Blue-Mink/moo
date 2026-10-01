// Package pipeline 的包体校验工具：sha256 与 manifest 版本读取。
// 用途：缓存复用前核验「这个文件真的是当前源当前版本的包」——
// 缓存文件名跨源共享（<appname>.fpk），源又存在「版本声明与包文件
// 不一致」（滚动覆盖同一文件）的数据问题，仅凭文件名/粗粒度大小
// 会把旧包当新包装上去（表现为升级成功但版本不变的假成功）。
package pipeline

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
)

// Sha256OfFile 计算文件 sha256（小写 hex）。
func Sha256OfFile(path string) (string, error) {
	fh, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer fh.Close()
	h := sha256.New()
	if _, err := io.Copy(h, fh); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ManifestVersion 读 FPK（tar.gz 优先、zip 兜底）manifest 里的 version。
// manifest 通常在包尾（大 app.tgz 之后），需顺序解压到条目，
// 大包耗时数秒——只在安装链路上按需调用。
func ManifestVersion(path string) (string, error) {
	data, err := readFpkManifestEntry(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.ToLower(strings.TrimSpace(k)) == "version" {
			return strings.TrimSpace(v), nil
		}
	}
	return "", errors.New("manifest 中没有 version 字段")
}

// readFpkManifestEntry 从 FPK 归档流式读取 manifest 条目。
func readFpkManifestEntry(path string) ([]byte, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()

	fh.Seek(0, io.SeekStart)
	gr, gerr := gzip.NewReader(fh)
	if gerr == nil {
		tr := tar.NewReader(gr)
		for {
			hdr, terr := tr.Next()
			if terr != nil {
				break
			}
			if hdr.Typeflag == tar.TypeReg &&
				(hdr.Name == "manifest" || strings.HasSuffix(hdr.Name, "/manifest")) {
				data, rerr := io.ReadAll(io.LimitReader(tr, 1<<20))
				if rerr != nil {
					return nil, rerr
				}
				gr.Close()
				return data, nil
			}
		}
		gr.Close()
	}

	fh.Seek(0, io.SeekStart)
	st, serr := fh.Stat()
	if serr != nil {
		return nil, serr
	}
	zr, zerr := zip.NewReader(fh, st.Size())
	if zerr != nil {
		return nil, zerr
	}
	for _, zf := range zr.File {
		name := strings.TrimPrefix(zf.Name, "./")
		if name == "manifest" || name == "manifest.json" || name == "fnpack.json" ||
			strings.HasSuffix(name, "/manifest") {
			rc, err := zf.Open()
			if err != nil {
				return nil, err
			}
			data, err := io.ReadAll(io.LimitReader(rc, 1<<20))
			rc.Close()
			if err != nil {
				return nil, err
			}
			return data, nil
		}
	}
	return nil, errors.New("manifest not found in package")
}
