// Package task 提供后台 FPK 下载任务：暂停/继续（.part 断点续传）/进度。
package task

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 任务状态。
const (
	StateRunning = "running"
	StatePaused  = "paused"
	StateDone    = "done"
	StateError   = "error"
)

// Task 是一个 FPK 下载任务。
type Task struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	AppName  string `json:"app_name"`
	FileName string `json:"file_name"`
	DestPath string `json:"dest_path"`
	PartPath string `json:"-"`
	State    string `json:"state"`
	Err      string `json:"error,omitempty"`

	Candidates []string // 候选 URL（偏好顺序），下载时按序试错
	SizeBytes  int64    // 0 = 未知
	SHA256     string   // 源声明包哈希（空 = 不校验）

	mu sync.Mutex
	done   int64
	size   int64 // 总大小，未知时为 0
	cancel context.CancelFunc
}

// Progress 返回 (已完成字节, 总字节, 状态, 错误)。
func (t *Task) Progress() (int64, int64, string, string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.done, t.size, t.State, t.Err
}

func (t *Task) setSize(size int64) {
	t.mu.Lock()
	if size > 0 {
		t.size = size
	}
	t.mu.Unlock()
}

// EnsureSize 引擎无法报告总大小（镜像无 Content-Length）时用探测值兜底：
// 无尺寸则前端算不出下载百分率。仅在当前未知时填充。
func (t *Task) EnsureSize(size int64) {
	if size <= 0 {
		return
	}
	t.mu.Lock()
	if t.size <= 0 {
		t.size = size
		t.SizeBytes = size
	}
	t.mu.Unlock()
}

func (t *Task) setDone(n int64) {
	t.mu.Lock()
	t.done = n
	t.mu.Unlock()
}

func (t *Task) setState(state, errMsg string) {
	t.mu.Lock()
	t.State = state
	t.Err = errMsg
	t.mu.Unlock()
}

// Manager 管理所有下载任务。
type Manager struct {
	mu          sync.Mutex
	tasks       map[string]*Task
	downloadDir string
	Engine      *Engine
}

// NewManager 创建任务管理器；downloadDir 不存在时创建。
func NewManager(downloadDir string, eng *Engine) (*Manager, error) {
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		return nil, err
	}
	if eng == nil {
		eng = NewEngine("")
	}
	// 清理旧版（0.5.0/0.5.1）遗留的 aria2 输入文件（新版改用 -o 参数，不再产生）
	if entries, err := os.ReadDir(downloadDir); err == nil {
		for _, ent := range entries {
			if strings.HasSuffix(ent.Name(), ".aria2-input") {
				_ = os.Remove(filepath.Join(downloadDir, ent.Name()))
			}
		}
	}
	return &Manager{
		tasks:       map[string]*Task{},
		downloadDir: downloadDir,
		Engine:      eng,
	}, nil
}

// DownloadDir 返回任务落盘目录。
func (m *Manager) DownloadDir() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.downloadDir
}

// SetDownloadDir 运行时切换落盘目录（设置页「FPK 下载目录」选择器用）。
// 调用方负责先迁移旧目录缓存文件并确认无进行中任务。
func (m *Manager) SetDownloadDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	m.mu.Lock()
	m.downloadDir = dir
	m.mu.Unlock()
	return nil
}

// HasActive 是否有进行中/已暂停的下载任务（切换下载目录前用于保护）。
func (m *Manager) HasActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tasks {
		t.mu.Lock()
		active := t.State == StateRunning || t.State == StatePaused
		t.mu.Unlock()
		if active {
			return true
		}
	}
	return false
}

// Cached 检查成品缓存是否可用（A3 缓存复用）：
// 文件存在、非空、无 aria2 续传残留（.aria2 控制文件），
// 且声明大小时与任一单位解释一致（best-effort 失效旧缓存）。
// 返回 (缓存路径, 是否命中)。
func (m *Manager) Cached(fileName string, declaredCandidates ...int64) (string, bool) {
	dest := filepath.Join(m.downloadDir, fileName)
	fi, err := os.Stat(dest)
	if err != nil || fi.Size() <= 0 {
		return "", false
	}
	if _, err := os.Stat(dest + ".aria2"); err == nil {
		return "", false // aria2 续传残留（未完成）
	}
	if !SizeMatches(fi.Size(), declaredCandidates...) {
		return "", false // 大小不符（旧版本/损坏）
	}
	return dest, true
}

// Start 新建并启动一个下载任务（后台 goroutine 执行）。
// candidates 为候选 URL（偏好顺序）；sizeBytes 未知时传 0。
// sha256 为源声明的包哈希（空 = 不校验；非空时下载成品逐一校验，
// 不匹配丢弃文件并试下一候选）。
func (m *Manager) Start(source, appName string, candidates []string, fileName string, sizeBytes int64, sha256 string) (*Task, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("无下载候选")
	}
	if fileName == "" {
		fileName = appName + ".fpk"
	}
	// 防路径穿越：只保留文件名
	fileName = filepath.Base(fileName)
	if !strings.HasSuffix(strings.ToLower(fileName), ".fpk") {
		fileName += ".fpk"
	}
	t := &Task{
		ID:         newID(),
		Source:     source,
		AppName:    appName,
		FileName:   fileName,
		DestPath:   filepath.Join(m.downloadDir, fileName),
		State:      StateRunning,
		Candidates: candidates,
		SizeBytes:  sizeBytes,
		SHA256:     sha256,
	}
	t.PartPath = t.DestPath + ".part"
	// 声明大小未知时先 HEAD 探测总大小（aria2 遇到无 Content-Length 的
	// 镜像不会报告尺寸，前端将没有百分率）。探测失败保持 0，
	// 前端回退显示已下载大小。
	if sizeBytes == 0 {
		sizeBytes = ProbeContentLength(candidates)
	}
	if sizeBytes > 0 {
		t.EnsureSize(sizeBytes)
	}
	m.mu.Lock()
	m.tasks[t.ID] = t
	m.mu.Unlock()
	go m.run(t)
	return t, nil
}

// newID 生成 8 字节随机任务 ID。
func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// run 执行下载（委托 Engine：aria2 多连接 + 候选镜像试错 + 断点续传）。
func (m *Manager) run(t *Task) {
	ctx, cancel := context.WithCancel(context.Background())
	t.mu.Lock()
	t.cancel = cancel
	t.mu.Unlock()
	defer cancel()

	err := m.Engine.Download(ctx, DownloadOpts{
		DestPath:   t.DestPath,
		Candidates: t.Candidates,
		SizeBytes:  t.SizeBytes,
		SHA256:     t.SHA256,
		Progress: func(done, size int64) {
			t.setDone(done)
			t.setSize(size)
		},
		OnFail: func(candidate, stage string, derr error) {
			log.Printf("下载候选失败 [%s/%s] %s: %v", t.AppName, stage, candidate, derr)
		},
	})
	switch {
	case err == nil:
		t.setState(StateDone, "")
	case ctx.Err() != nil:
		t.setState(StatePaused, "") // 暂停：已写部分保留供续传
	default:
		t.setState(StateError, err.Error())
	}
}

// Pause 暂停运行中的任务。
func (m *Manager) Pause(id string) error {
	t := m.get(id)
	if t == nil {
		return &ErrNotFound{ID: id}
	}
	t.mu.Lock()
	if t.State != StateRunning || t.cancel == nil {
		state := t.State
		t.mu.Unlock()
		return &ErrBadState{ID: id, State: state}
	}
	t.cancel()
	t.mu.Unlock()
	// 等待 run 落盘并置终态（它负责最终状态）
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, _, state, _ := t.Progress()
		if state == StatePaused || state == StateError {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil
}

// Resume 继续已暂停的任务（.part 已有字节由 Range 续传）。
func (m *Manager) Resume(id string) error {
	t := m.get(id)
	if t == nil {
		return &ErrNotFound{ID: id}
	}
	t.mu.Lock()
	state := t.State
	t.mu.Unlock()
	if state != StatePaused {
		return &ErrBadState{ID: id, State: state}
	}
	t.setState(StateRunning, "")
	go m.run(t)
	return nil
}

// Get 查任务。
func (m *Manager) Get(id string) *Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tasks[id]
}

// FindByApp 按应用名找最近一个非终态任务（下载暂停/继续按应用名定位）。
func (m *Manager) FindByApp(appName string) *Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best *Task
	for _, t := range m.tasks {
		if t.AppName != appName {
			continue
		}
		t.mu.Lock()
		st := t.State
		t.mu.Unlock()
		if st == StateDone || st == StateError {
			continue
		}
		if best == nil {
			best = t
		}
	}
	return best
}

func (m *Manager) get(id string) *Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tasks[id]
}

// List 列出全部任务。
func (m *Manager) List() []*Task {
	m.mu.Lock()
	out := make([]*Task, 0, len(m.tasks))
	for _, t := range m.tasks {
		out = append(out, t)
	}
	m.mu.Unlock()
	return out
}

// View 是任务的对外视图（含实时进度与百分比）。
type View struct {
	ID       string  `json:"id"`
	Source   string  `json:"source"`
	AppName  string  `json:"app_name"`
	FileName string  `json:"file_name"`
	DestPath string  `json:"dest_path"`
	State    string  `json:"state"`
	Err      string  `json:"error,omitempty"`
	Done     int64   `json:"done"`
	Size     int64   `json:"size"`
	Percent  float64 `json:"percent"`
}

// ViewOf 返回单个任务的视图。
func ViewOf(t *Task) View {
	done, size, state, errMsg := t.Progress()
	v := View{
		ID:       t.ID,
		Source:   t.Source,
		AppName:  t.AppName,
		FileName: t.FileName,
		DestPath: t.DestPath,
		State:    state,
		Err:      errMsg,
		Done:     done,
		Size:     size,
	}
	if size > 0 {
		v.Percent = float64(done) / float64(size) * 100
		if v.Percent > 100 {
			v.Percent = 100
		}
	}
	return v
}

// Views 返回全部任务的视图。
func (m *Manager) Views() []View {
	tasks := m.List()
	out := make([]View, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, ViewOf(t))
	}
	return out
}

// Remove 从任务列表移除（0.6.146 失败任务可清除；0.6.147 全部任务可删除）。
// 运行中任务先暂停（取消 ctx，等 run 落终态 ≤3s）再移除；
// .part 断点残留随任务删除，成品 .fpk 保留（由 FPK 下载目录文件列表管理）。
func (m *Manager) Remove(id string) error {
	t := m.get(id)
	if t == nil {
		return &ErrNotFound{ID: id}
	}
	t.mu.Lock()
	state := t.State
	cancel := t.cancel
	t.mu.Unlock()
	if state == StateRunning && cancel != nil {
		cancel()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			t.mu.Lock()
			st := t.State
			t.mu.Unlock()
			if st != StateRunning {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	// 重读终态：等待窗口内下载可能恰好完成（done 必须保留成品）
	t.mu.Lock()
	finalState := t.State
	t.mu.Unlock()
	m.mu.Lock()
	delete(m.tasks, id)
	m.mu.Unlock()
	// 清理断点残留（0.6.147 实测修正：aria2 路径直接写 DestPath 最终名 +
	// .aria2 控制文件，内置单连接回退用 .part——三种都要清）：
	//   - .part  内置单连接续传文件
	//   - .aria2 aria2 控制文件
	//   - DestPath 未完成的本体（done 态是成品，保留，由下载目录文件列表管理）
	_ = os.Remove(t.PartPath)
	_ = os.Remove(t.DestPath + ".aria2")
	if finalState != StateDone {
		_ = os.Remove(t.DestPath)
	}
	return nil
}

// ErrNotFound 任务不存在。
type ErrNotFound struct{ ID string }

func (e *ErrNotFound) Error() string { return "任务不存在: " + e.ID }

// ErrBadState 任务状态不允许该操作。
type ErrBadState struct {
	ID    string
	State string
}

func (e *ErrBadState) Error() string {
	return "任务状态不允许该操作: " + e.ID + " (当前 " + e.State + ")"
}
