package source

// SeedCacheForTest 测试入口：直接注入若干源的目录缓存（绕过网络抓取）。
// api 包单测无法访问 Manager.cache 未导出字段，统一走这里种缓存；
// fresh 时间不改动（轮转预算相关断言另行直接操作 fresh）。
// 仅限单测使用，生产代码不得调用。
func (m *Manager) SeedCacheForTest(c map[string]map[string]*App) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range c {
		m.cache[k] = v
	}
}
