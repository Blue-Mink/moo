import { describe, it, expect } from 'vitest';
import { parseLine, collapseDetail, COLLAPSE_CHARS } from './logParse';

describe('parseLine 判级', () => {
  it('标准行：时间 + 模块 + 详情', () => {
    const p = parseLine('2026/10/20 01:53:49.120856 [fpk-local] 全量索引完成: 138 个包');
    expect(p.timeShort).toBe('10-20 01:53:49');
    expect(p.timeClock).toBe('01:53:49');
    expect(p.mod).toBe('fpk-local');
    expect(p.detail).toBe('全量索引完成: 138 个包');
    expect(p.level).toBe('info');
  });

  it('无时间戳行（生命周期脚本输出）', () => {
    const p = parseLine('Stopping moo ...');
    expect(p.timeShort).toBe('');
    expect(p.time).toBe('');
    expect(p.detail).toBe('Stopping moo ...');
  });

  it('零失败摘要不判 ERROR（历史坑）', () => {
    expect(parseLine('2026/10/20 01:00:00.1 [fpk-local] 156 成功 / 0 失败').level).toBe('info');
  });

  it('真实失败仍判 ERROR', () => {
    expect(parseLine('2026/10/20 01:00:00.1 [fpk-local] 3 失败').level).toBe('error');
  });

  it('0.6.343 iconfail 压缩摘要的计数片段不判 ERROR', () => {
    const line = '2026/10/20 01:53:19.1 [iconfail] brian099@phpMyAdmin decl="" [https://a/x.png(ok=false,miss=true) …]（共 24 条，确定缺失 6 / 其它 18）';
    expect(parseLine(line).level).toBe('info');
  });

  it('旧日志里的「网络失败 N」计数同样不判 ERROR', () => {
    const line = '2026/10/20 01:53:19.1 [iconfail] brian099@phpMyAdmin decl="" 候选=[…]（共 24 条，确定缺失 6 / 网络失败 18）';
    expect(parseLine(line).level).toBe('info');
  });

  it('verbose 模块归 DEBUG', () => {
    expect(parseLine('2026/10/20 01:00:00.1 [race] 60s: 209 次竞速').level).toBe('debug');
  });
});

describe('collapseDetail 折叠', () => {
  it('短行不折叠', () => {
    const r = collapseDetail('预热完成: 174/182 命中');
    expect(r.truncated).toBe(false);
    expect(r.text).toBe('预热完成: 174/182 命中');
  });

  it('超长行截断并标记', () => {
    const long = 'x'.repeat(COLLAPSE_CHARS + 50);
    const r = collapseDetail(long);
    expect(r.truncated).toBe(true);
    expect(r.text.length).toBeLessThanOrEqual(COLLAPSE_CHARS + 1);
  });

  it('候选列表压成计数摘要（不含候选 URL）', () => {
    const detail = 'brian099@phpMyAdmin decl="https://cdn.gh-proxy.org/https://raw.githubusercontent.com/B/x.png" 候选=[' +
      Array.from({ length: 6 }, (_, i) => `https://m${i}/x.png(ok=false,miss=${i < 2})`).join(' ') + ']';
    const r = collapseDetail(detail);
    expect(r.truncated).toBe(true);
    expect(r.text).toContain('候选 6 个');
    expect(r.text).toContain('404 2');
    // head（应用标识 + 声明地址）最多保留 96 字符；候选清单本身不再展开
    expect(r.text).not.toContain('(ok=false');
    expect(r.text).not.toContain('m0/x.png');
    expect(r.text.length).toBeLessThan(220);
  });
});

describe('parseLine · [mirror] 每轮摘要不判错（2026-11-26 用户实报）', () => {
  const round = (ok: number, fail: number, extra: string) =>
    `2026/11/26 12:40:15.742118 [mirror] gh 探测 12 源 ok=${ok} fail=${fail} limited=0 优选=gh-proxy-org 耗时=20s（异常：${extra}）`;

  it('部分镜像超时/403 属常态波动 → info（不刷红）', () => {
    const p = parseLine(round(8, 4, 'gh-tbap=超时，ghfast=超时，yylx=超时，conversun=403 拒绝'));
    expect(p.mod).toBe('mirror');
    expect(p.level).toBe('info');
  });

  it('本轮全挂（ok=0）→ warn（真有事，但不升成 ERROR）', () => {
    const p = parseLine(round(0, 12, 'direct=连接超时，ghfast=超时'));
    expect(p.level).toBe('warn');
  });

  it('成功率不足一半 / 出现限流 → warn（批次 C 阈值）', () => {
    const low = parseLine(round(3, 9, 'ghfast=超时'));
    expect(low.level).toBe('warn');
    const limited = parseLine('2026/11/26 14:20:00.000000 [mirror] gh 探测 12 源 ok=10 fail=0 limited=2 优选=direct 耗时=3s');
    expect(limited.level).toBe('warn');
    const half = parseLine(round(6, 6, 'ghfast=超时'));
    expect(half.level).toBe('info');
  });

  it('[list-sync] 每轮摘要是进度而非错误（失败 0 → info、有失败 → warn）', () => {
    const zero = parseLine('2026/11/26 13:44:22.335233 [list-sync] 本轮完成：列表 105 条 / 已存在 105 / 新增 0 / 失败 0 / 耗时 237ms');
    expect(zero.level).toBe('info');
    const some = parseLine('2026/11/26 13:44:22.335233 [list-sync] 本轮完成：列表 105 条 / 已存在 100 / 新增 3 / 失败 2 / 耗时 2.1s');
    expect(some.level).toBe('warn');
  });

  it('同样的词出现在别处仍是 ERROR（只豁免 mirror 摘要行）', () => {
    const p = parseLine('2026/11/26 12:41:00 [fpk-local] 下载失败：conversun 403 拒绝');
    expect(p.level).toBe('error');
  });
});
