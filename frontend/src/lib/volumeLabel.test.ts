import { describe, expect, it } from 'vitest'
import { volumeOptionLabel } from './volumeLabel'

describe('volumeOptionLabel（0.6.325 C1 卷显示名优先级，表驱动）', () => {
  const cases: { name?: string; path?: string; index: number; want: string }[] = [
    // 有友好名：友好名 · 路径
    { name: '工作盘', path: '/vol1', index: 1, want: '工作盘 · /vol1' },
    { name: '工作盘', path: '', index: 1, want: '工作盘' },
    { name: '  ', path: '/vol2', index: 2, want: '存储空间 2 · /vol2' }, // 纯空白=无
    // 无友好名（后端当前恒空）：存储空间 N · 路径（C1 前行为，零变化）
    { name: undefined, path: '/vol2', index: 2, want: '存储空间 2 · /vol2' },
    { name: undefined, path: '', index: 1, want: '存储空间 1' },
    { name: undefined, path: undefined, index: 8, want: '存储空间 8' },
    // 双卷可辨识（G4 场景：卷号 + /volN）
    { name: undefined, path: '/vol1', index: 1, want: '存储空间 1 · /vol1' },
    { name: undefined, path: '/vol2', index: 2, want: '存储空间 2 · /vol2' },
  ]
  for (const c of cases) {
    it(`${JSON.stringify(c)} → ${c.want}`, () => {
      expect(volumeOptionLabel({ index: c.index, path: c.path, name: c.name })).toBe(c.want)
    })
  }
})
