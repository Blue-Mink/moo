import { describe, expect, it } from 'vitest'
import { appRunState, canStartInList, installedStateLabel, isStoppedState } from './appRunState'

describe('appRunState（0.6.342 列表运行态口径）', () => {
  it('未安装恒为空（列表未装卡不显示运行态）', () => {
    expect(appRunState({ installed: false, status: 'stopped' })).toBe('')
    expect(appRunState(undefined)).toBe('')
    expect(appRunState(null)).toBe('')
  })

  it('已安装透传 daemon 状态，未知/空值原样返回', () => {
    expect(appRunState({ installed: true, status: 'stopped' })).toBe('stopped')
    expect(appRunState({ installed: true, status: 'running' })).toBe('running')
    expect(appRunState({ installed: true, status: 'nostart' })).toBe('nostart')
    expect(appRunState({ installed: true, status: 'start' })).toBe('start')
    expect(appRunState({ installed: true })).toBe('')
  })
})

describe('installedStateLabel（列表徽章文案）', () => {
  it('停用态显示「已停止」，过渡态显示启动中/停用中', () => {
    expect(installedStateLabel({ installed: true, status: 'stopped' })).toBe('已停止')
    expect(installedStateLabel({ installed: true, status: 'starting' })).toBe('启动中')
    expect(installedStateLabel({ installed: true, status: 'stopping' })).toBe('停用中')
  })

  it('0.6.348：运行中/系统组件如实显示（与桌面网页卡同一口径）', () => {
    expect(installedStateLabel({ installed: true, status: 'running' })).toBe('运行中')
    expect(installedStateLabel({ installed: true, status: 'nostart' })).toBe('未启动')
    // 历史 start 值（探测不出真实进程时保留）仍按「已安装」处理
    expect(installedStateLabel({ installed: true, status: 'start' })).toBe('已安装')
    expect(installedStateLabel({ installed: true })).toBe('已安装')
  })

  it('未安装显示「未安装」', () => {
    expect(installedStateLabel({ installed: false, status: 'stopped' })).toBe('未安装')
    expect(installedStateLabel(undefined)).toBe('未安装')
  })
})

describe('canStartInList（列表启动按钮判定）', () => {
  it('已装 + 支持启停 + 已停止 → 出启动按钮', () => {
    expect(canStartInList({ installed: true, status: 'stopped', start_stop: true })).toBe(true)
    // 缺省 start_stop 按支持处理（与既有「打开」判定同口径）
    expect(canStartInList({ installed: true, status: 'stopped' })).toBe(true)
  })

  it('不支持启停（nostart 系统组件）不给启动按钮', () => {
    expect(canStartInList({ installed: true, status: 'nostart', start_stop: false })).toBe(false)
    expect(canStartInList({ installed: true, status: 'stopped', start_stop: false })).toBe(false)
  })

  it('运行中 / 过渡态 / 未安装不给启动按钮', () => {
    expect(canStartInList({ installed: true, status: 'running', start_stop: true })).toBe(false)
    expect(canStartInList({ installed: true, status: 'starting', start_stop: true })).toBe(false)
    expect(canStartInList({ installed: true, status: 'stopping', start_stop: true })).toBe(false)
    expect(canStartInList({ installed: false, status: 'stopped', start_stop: true })).toBe(false)
  })
})

describe('isStoppedState（警示色判定）', () => {
  it('停用与过渡态为警示色，运行/系统组件/未安装否', () => {
    expect(isStoppedState({ installed: true, status: 'stopped' })).toBe(true)
    expect(isStoppedState({ installed: true, status: 'starting' })).toBe(true)
    expect(isStoppedState({ installed: true, status: 'stopping' })).toBe(true)
    expect(isStoppedState({ installed: true, status: 'running' })).toBe(false)
    expect(isStoppedState({ installed: true, status: 'nostart' })).toBe(false)
    expect(isStoppedState({ installed: false, status: 'stopped' })).toBe(false)
  })
})
