import { describe, expect, it } from 'vitest'

import { OFFICIAL_SOURCE_ID, isPlainHttp, shouldWarnPlainHttp } from './sourceWarnings'

describe('isPlainHttp', () => {
  it('识别 http:// 明文地址', () => {
    expect(isPlainHttp('http://example.com/app.json')).toBe(true)
    expect(isPlainHttp('HTTP://EXAMPLE.COM/x')).toBe(true)
  })

  it('https / 无 scheme / 空值 不算明文', () => {
    expect(isPlainHttp('https://example.com/app.json')).toBe(false)
    expect(isPlainHttp('example.com/x/fndepot.json')).toBe(false)
    expect(isPlainHttp('')).toBe(false)
    expect(isPlainHttp(undefined)).toBe(false)
  })
})

describe('shouldWarnPlainHttp', () => {
  it('官方应用源豁免——即使是 http 也不显示感叹号', () => {
    expect(
      shouldWarnPlainHttp({ id: OFFICIAL_SOURCE_ID, url: 'http://127.0.0.1:5666/app/moo/official.json' }),
    ).toBe(false)
  })

  it('其它 http 明文源仍显示感叹号', () => {
    expect(shouldWarnPlainHttp({ id: 'community-a', url: 'http://example.com/fndepot.json' })).toBe(true)
    expect(shouldWarnPlainHttp({ id: 'my-lan-mirror', url: 'http://192.0.2.10/fndepot.json' })).toBe(true)
  })

  it('https 源不显示', () => {
    expect(shouldWarnPlainHttp({ id: 'community-b', url: 'https://example.com/fndepot.json' })).toBe(false)
  })

  it('缺 url 的源不显示', () => {
    expect(shouldWarnPlainHttp({ id: 'community-c' })).toBe(false)
  })
})
