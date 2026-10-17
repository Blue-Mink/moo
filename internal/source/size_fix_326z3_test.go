package source

import (
	"strings"
	"testing"
)

// 0.6.326z3：fnmusic-ext 实测形态——顶层 version/download_url/sha256 +
// 条目级 size 误填字节（协议要求 MB）+ 包级 size 正确（字节）。
// 修前：包级被门控读不到 → 条目级 27257554 被当 MB ×1e6 → 详情页显示
// 25385.61 GB；修后：按字节采信 = 26.0 MB。
func TestHybridEntrySizeWrittenAsBytes(t *testing.T) {
	const url = "https://github.com/javycoder/fnos_music_ext/releases/download/v2.9.0/fnmusic-ext-2.9.0.fpk"
	doc := []byte(`{
	  "schema_version": "moo",
	  "source_info": {"name": "fnmusic-ext 官方源"},
	  "apps": {
	    "fnmusic-ext": {
	      "display_name": "fnMusic 音源扩展",
	      "version": "2.9.0",
	      "download_url": "` + url + `",
	      "sha256": "861748417de611ec723f1c5dbfa7ab10d6c150ef9843db191944909ce493bd25",
	      "size": 27257554,
	      "releases": {
	        "2.9.0": {
	          "packages": {
	            "x86": {"download_url": "` + url + `", "size": 27257554,
	                    "sha256": "861748417de611ec723f1c5dbfa7ab10d6c150ef9843db191944909ce493bd25"},
	            "arm": {"download_url": "` + url + `", "size": 27257554}
	          }
	        }
	      }
	    }
	  }
	}`)
	entries, err := ParseFnpack(doc)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	m, ok := entries["fnmusic-ext"]
	if !ok {
		t.Fatalf("未解析到 fnmusic-ext: %v", entries)
	}
	app := translateEntry("fnmusic-ext", m, "javycoder", "https://github.com/javycoder/fnos_music_ext", "")
	if app.SizeBytes != 27257554 {
		t.Errorf("SizeBytes = %d, want 27257554（字节误写进 MB 槽位应按字节采信）", app.SizeBytes)
	}
	if app.SizeMB != "27257554" {
		t.Errorf("SizeMB = %q, want 保留源声明原值（读取侧归一，不改写声明）", app.SizeMB)
	}
	if app.Version != "2.9.0" || !strings.Contains(app.DownloadURL, "v2.9.0") {
		t.Errorf("顶层字段被破坏: version=%q url=%q", app.Version, app.DownloadURL)
	}
}

// 混合形态（顶层 version/download_url）而无条目级体积时，必须能读到包级字节值。
func TestHybridEntryPackageSizeFallback(t *testing.T) {
	m := mustJSON(t, map[string]any{
		"display_name": "混合形态应用",
		"version":      "4.8.8-1",
		"download_url": "https://example.com/app.fpk",
		"releases": map[string]any{
			"4.8.8-1": map[string]any{
				"packages": map[string]any{
					"all": map[string]any{"download_url": "https://example.com/app.fpk", "size": 4996847},
				},
			},
		},
	})
	a := translateEntry("hybrid", m, "src", "https://example.com", "")
	if a.SizeBytes != 4996847 {
		t.Errorf("SizeBytes = %d, want 4996847（包级字节值兜底）", a.SizeBytes)
	}
}

// 条目级是正常 MB 值（如 4.8）时，包级字节值更精确，应被采用。
func TestHybridEntryPackageSizePreferred(t *testing.T) {
	m := mustJSON(t, map[string]any{
		"version":      "0.6.326",
		"download_url": "https://example.com/moo.fpk",
		"size":         4.8,
		"releases": map[string]any{
			"0.6.326": map[string]any{
				"packages": map[string]any{
					"x86": map[string]any{"download_url": "https://example.com/moo.fpk", "size": 4996847},
				},
			},
		},
	})
	a := translateEntry("moo", m, "src", "https://example.com", "")
	if a.SizeBytes != 4996847 {
		t.Errorf("SizeBytes = %d, want 4996847（包级字节值优先）", a.SizeBytes)
	}
	if a.SizeMB != "4.8" {
		t.Errorf("SizeMB = %q, want 4.8", a.SizeMB)
	}
}

// 包级 size 被源写成 MB 小数（26.0）时不可当字节采信（否则显示 0.0 MB）。
func TestPackageSizeWrittenInMBRejected(t *testing.T) {
	m := mustJSON(t, map[string]any{
		"version":      "1.0.0",
		"download_url": "https://example.com/a.fpk",
		"releases": map[string]any{
			"1.0.0": map[string]any{
				"packages": map[string]any{
					"x86": map[string]any{"download_url": "https://example.com/a.fpk", "size": 26.0},
				},
			},
		},
	})
	a := translateEntry("a", m, "src", "https://example.com", "")
	if a.SizeBytes != 0 {
		t.Errorf("SizeBytes = %d, want 0（26 字节不可信，交条目级兜底）", a.SizeBytes)
	}
}

// 正常 MB 声明（无 releases）保持原样：SizeBytes 留 0，交上层按 MB×1e6 解析。
func TestNormalMBEntryUnchanged(t *testing.T) {
	m := mustJSON(t, map[string]any{
		"display_name": "老应用",
		"version":      "1.0.0",
		"download_url": "https://example.com/a.fpk",
		"size":         "12.3",
	})
	a := translateEntry("a", m, "src", "https://example.com", "")
	if a.SizeBytes != 0 || a.SizeMB != "12.3" {
		t.Errorf("正常 MB 值行为被改: SizeBytes=%d SizeMB=%q", a.SizeBytes, a.SizeMB)
	}
}

// 取包级体积时必须按条目版本取，不能串到其它更新的版本上。
func TestPackageSizePicksEntryVersion(t *testing.T) {
	m := mustJSON(t, map[string]any{
		"version":      "2.8.0",
		"download_url": "https://example.com/2.8.0.fpk",
		"releases": map[string]any{
			"2.9.0": map[string]any{"packages": map[string]any{
				"x86": map[string]any{"download_url": "https://example.com/2.9.0.fpk", "size": 27257554}}},
			"2.8.0": map[string]any{"packages": map[string]any{
				"x86": map[string]any{"download_url": "https://example.com/2.8.0.fpk", "size": 27165647}}},
		},
	})
	a := translateEntry("x", m, "src", "https://example.com", "")
	if a.SizeBytes != 27165647 {
		t.Errorf("SizeBytes = %d, want 27165647（应取条目版本 2.8.0 的包）", a.SizeBytes)
	}
}
