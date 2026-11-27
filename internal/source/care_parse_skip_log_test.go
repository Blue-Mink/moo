package source

// 2026-11-27（F11/N12 复核 M2）：「0 条且索引结构未识别」的日志只在首轮记一行。
//
// 原实现用 `m.careEmpty[st.Name] == 0` 当「首轮」判据，但该分支从不自增计数
// → 条件恒真，上游索引格式坏掉时每轮都打一行（复核实跑 10 轮 10 行）。
// 修复：改用独立标记 careParseSkipLogged，真看到应用（Count>0）后清除。

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestCareParseFailedLogsOncePerEpisode(t *testing.T) {
	m, _ := careManager(t, false)
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })

	fail := []SourceStatus{
		{Name: "a", Count: 0, ParseFailed: true},
		{Name: "b", Count: 2},
		{Name: "c", Count: 2},
	}
	for i := 0; i < 10; i++ {
		m.AutoCarePass(fail)
	}
	if got := strings.Count(buf.String(), "索引结构未识别"); got != 1 {
		t.Fatalf("同一段解析故障只应记 1 行日志（旧实现每轮一行），实际 %d 行:\n%s", got, buf.String())
	}

	// 真看到应用 → 清除标记；再次故障应重新记一行（累计 2 行）。
	m.AutoCarePass([]SourceStatus{
		{Name: "a", Count: 3},
		{Name: "b", Count: 2},
		{Name: "c", Count: 2},
	})
	m.AutoCarePass(fail)
	if got := strings.Count(buf.String(), "索引结构未识别"); got != 2 {
		t.Fatalf("故障恢复后再犯应重新记一行（累计 2 行），实际 %d 行:\n%s", got, buf.String())
	}
}
