import { describe, expect, it } from 'vitest';
import { shouldShowUpdatePrompts, updateBadgeKind, updateIssueCount } from './updatePrompts';

describe('shouldShowUpdatePrompts', () => {
  it('仅「有更新」页返回 true', () => {
    expect(shouldShowUpdatePrompts('update_available')).toBe(true);
  });

  it('全部 / 已安装 / 发现 三个 tab 均返回 false', () => {
    expect(shouldShowUpdatePrompts('all')).toBe(false);
    expect(shouldShowUpdatePrompts('installed')).toBe(false);
    expect(shouldShowUpdatePrompts('recommended')).toBe(false);
  });

  it('未知取值保守返回 false（宁可不显示，也不在别的页面冒横幅）', () => {
    expect(shouldShowUpdatePrompts('')).toBe(false);
    expect(shouldShowUpdatePrompts('nope')).toBe(false);
  });
});

describe('updateIssueCount', () => {
  it('手动项 + 自更新异常 = 总数', () => {
    expect(updateIssueCount(3, true)).toBe(4);
    expect(updateIssueCount(3, false)).toBe(3);
    expect(updateIssueCount(0, true)).toBe(1);
  });

  it('0 / 负数 / 非有限数按 0', () => {
    expect(updateIssueCount(0, false)).toBe(0);
    expect(updateIssueCount(-5, false)).toBe(0);
    expect(updateIssueCount(-5, true)).toBe(1);
    expect(updateIssueCount(Number.NaN, false)).toBe(0);
    expect(updateIssueCount(Number.POSITIVE_INFINITY, false)).toBe(0);
  });
});

describe('updateBadgeKind', () => {
  it('有可更新应用 → red（即使同时有手动项）', () => {
    expect(updateBadgeKind({ updateCount: 2, manualCount: 0, selfUpdateProblem: false })).toBe('red');
    expect(updateBadgeKind({ updateCount: 2, manualCount: 5, selfUpdateProblem: true })).toBe('red');
  });

  it('无应用可更新但有手动项 / 自更新异常 → amber', () => {
    expect(updateBadgeKind({ updateCount: 0, manualCount: 1, selfUpdateProblem: false })).toBe('amber');
    expect(updateBadgeKind({ updateCount: 0, manualCount: 0, selfUpdateProblem: true })).toBe('amber');
  });

  it('都没有 → none（含脏数值）', () => {
    expect(updateBadgeKind({ updateCount: 0, manualCount: 0, selfUpdateProblem: false })).toBe('none');
    expect(updateBadgeKind({ updateCount: -1, manualCount: -3, selfUpdateProblem: false })).toBe('none');
    expect(updateBadgeKind({ updateCount: Number.NaN, manualCount: Number.NaN, selfUpdateProblem: false })).toBe('none');
  });
});
