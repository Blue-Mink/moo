import { describe, expect, it } from 'vitest'
import { installTypeLabel, installTypeRow } from './appMeta'

describe('installTypeLabel（运行方式展示口径）', () => {
  it('package 显示为用户空间（用户定稿）', () => {
    expect(installTypeLabel('package')).toBe('用户空间')
    expect(installTypeLabel('PACKAGE')).toBe('用户空间')
    expect(installTypeLabel('user-space')).toBe('用户空间')
    expect(installTypeLabel('用户空间')).toBe('用户空间')
  })

  it('root 原样显示', () => {
    expect(installTypeLabel('root')).toBe('root')
    expect(installTypeLabel('ROOT')).toBe('root')
  })

  it('system 显示为系统空间，其余未知值原样透传', () => {
    expect(installTypeLabel('system')).toBe('系统空间')
    expect(installTypeLabel('系统空间')).toBe('系统空间')
    expect(installTypeLabel('whatever')).toBe('whatever')
    expect(installTypeLabel('')).toBe('')
    expect(installTypeLabel(undefined)).toBe('')
  })
})

describe('installTypeRow（自适应标签 + 白名单）', () => {
  it('root/package 走「运行方式」', () => {
    expect(installTypeRow('root')).toEqual({ label: '运行方式', value: 'root' })
    expect(installTypeRow('package')).toEqual({ label: '运行方式', value: '用户空间' })
    expect(installTypeRow('用户空间')).toEqual({ label: '运行方式', value: '用户空间' })
  })
  it('存储空间/系统空间 走「安装位置」', () => {
    expect(installTypeRow('存储空间')).toEqual({ label: '安装位置', value: '存储空间' })
    expect(installTypeRow('系统空间')).toEqual({ label: '安装位置', value: '系统空间' })
    expect(installTypeRow('volume')).toEqual({ label: '安装位置', value: '存储空间' })
  })
  it('认不出来的杂值不显示（源里被塞了分类名）', () => {
    expect(installTypeRow('影视')).toBeNull()
    expect(installTypeRow('工具')).toBeNull()
    expect(installTypeRow('docker')).toBeNull()
    expect(installTypeRow('')).toBeNull()
    expect(installTypeRow(undefined)).toBeNull()
  })
})
