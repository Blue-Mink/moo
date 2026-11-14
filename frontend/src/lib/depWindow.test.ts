import { describe, it, expect } from 'vitest';
import {
  DEP_WINDOW_MIN_MINUTES,
  parseHHMM,
  depWindowSpanMinutes,
  depWindowIssue,
  formatSpanMinutes,
} from './depWindow';

// 0.6.551：自动停用时段前端校验 —— 断言与后端
// internal/api/autoupdate_deps.go::validateAutoUpdateWindow 同口径。
// 反向验证：把 depWindowIssue 里 span 判断阈值改成 30 → 「10:00-10:30」用例必红。
describe('depWindow（自动停用时段校验）', () => {
  it('常量与后端 depWindowMinMinutes 一致', () => {
    expect(DEP_WINDOW_MIN_MINUTES).toBe(60);
  });

  it('parseHHMM 接受 00:00-23:59，拒绝越界与畸形', () => {
    expect(parseHHMM('00:00')).toEqual([0, 0]);
    expect(parseHHMM('23:59')).toEqual([23, 59]);
    expect(parseHHMM(' 08:30 ')).toEqual([8, 30]);
    expect(parseHHMM('24:00')).toBeNull();
    expect(parseHHMM('12:60')).toBeNull();
    expect(parseHHMM('8:30')).toBeNull();
    expect(parseHHMM('')).toBeNull();
  });

  it('跨度按 +1440 取模（跨零点=次日）', () => {
    expect(depWindowSpanMinutes('00:00', '23:59')).toBe(1439);
    expect(depWindowSpanMinutes('09:00', '12:00')).toBe(180);
    expect(depWindowSpanMinutes('22:00', '02:00')).toBe(240); // 跨零点 4 小时
    expect(depWindowSpanMinutes('00:00', '00:00')).toBe(0);
    expect(depWindowSpanMinutes('bad', '02:00')).toBe(-1);
  });

  it('合法时段返回空串', () => {
    expect(depWindowIssue('00:00', '23:59')).toBe('');
    expect(depWindowIssue('01:00', '02:00')).toBe(''); // 正好 1 小时
    expect(depWindowIssue('22:00', '02:00')).toBe(''); // 跨零点 4 小时
  });

  it('开始 == 结束 → 明确报「不能相同」', () => {
    expect(depWindowIssue('10:00', '10:00')).toBe('开始与结束时间不能相同');
  });

  it('不足 1 小时 → 报时长，并说明当前跨度', () => {
    expect(depWindowIssue('10:00', '10:30')).toBe('时段不得短于 1 小时（当前 30 分）');
    expect(depWindowIssue('23:30', '00:00')).toBe('时段不得短于 1 小时（当前 30 分）');
  });

  it('格式非法 → 报格式（后端同文案口径）', () => {
    expect(depWindowIssue('', '23:59')).toBe('时段格式应为 HH:MM（00:00–23:59）');
  });

  it('formatSpanMinutes 人类可读', () => {
    expect(formatSpanMinutes(150)).toBe('2 小时 30 分');
    expect(formatSpanMinutes(120)).toBe('2 小时');
    expect(formatSpanMinutes(45)).toBe('45 分');
    expect(formatSpanMinutes(-1)).toBe('—');
  });
});
