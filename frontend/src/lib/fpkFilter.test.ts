import { describe, expect, it } from 'vitest';
import { filterFpkDownloads } from './fpkFilter';

const files = [
  { name: 'moo_0.6.570_x86.fpk', appname: 'moo', display_name: 'Moo', version: '0.6.570', source: 'Blue-Mink/FnDepot' },
  { name: 'QwenPaw_2.3.0_x86.fpk', appname: 'QwenPaw', display_name: 'QwenPaw', version: '2.3.0', source: 'naspk-com/QwenPaw' },
  { name: 'global-radio_1.5.4_x86.fpk', appname: 'global-radio', version: '1.5.4' },
];

describe('filterFpkDownloads（已下载 FPK 搜索）', () => {
  it('空查询原样返回', () => {
    expect(filterFpkDownloads(files, '   ')).toHaveLength(3);
  });
  it('按应用名搜索（大小写不敏感）', () => {
    expect(filterFpkDownloads(files, 'qwenpaw').map((f) => f.appname)).toEqual(['QwenPaw']);
  });
  it('按应用源名搜索', () => {
    expect(filterFpkDownloads(files, 'blue-mink').map((f) => f.appname)).toEqual(['moo']);
    expect(filterFpkDownloads(files, 'naspk')).toHaveLength(1);
  });
  it('按包文件名 / 版本搜索', () => {
    expect(filterFpkDownloads(files, 'global-radio')).toHaveLength(1);
    expect(filterFpkDownloads(files, '1.5.4')).toHaveLength(1);
  });
  it('无匹配返回空数组（不是全部）', () => {
    expect(filterFpkDownloads(files, 'zzz')).toEqual([]);
  });
});
