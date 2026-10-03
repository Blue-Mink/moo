import { describe, expect, it } from 'vitest';
import { isLinkLike, sourceKey } from './sourceKey';

const ROOT = 'github.com/ctllo-bit/fndepot';

describe('sourceKey 归一化（0.6.198）', () => {
  it('仓库根链接（各种写法）→ github.com/owner/repo', () => {
    expect(sourceKey('https://github.com/ctllo-bit/FnDepot')).toBe(ROOT);
    expect(sourceKey('https://github.com/ctllo-bit/FnDepot/')).toBe(ROOT);
    expect(sourceKey('https://github.com/ctllo-bit/FnDepot.git')).toBe(ROOT);
    expect(sourceKey('github.com/ctllo-bit/FnDepot')).toBe(ROOT); // 无协议
    expect(sourceKey('HTTPS://GITHUB.COM/ctllo-bit/FnDepot')).toBe(ROOT); // 大小写
    expect(sourceKey('https://github.com/ctllo-bit/FnDepot/tree/main')).toBe(ROOT);
    expect(sourceKey('https://github.com/ctllo-bit/FnDepot?tab=code')).toBe(ROOT);
  });

  it('raw 前缀链接（raw.githubusercontent.com，任意分支/文件）', () => {
    expect(sourceKey('https://raw.githubusercontent.com/ctllo-bit/FnDepot/main/fnpack.json')).toBe(ROOT);
    expect(sourceKey('https://raw.githubusercontent.com/ctllo-bit/FnDepot/master/fnpack.json')).toBe(ROOT);
    expect(sourceKey('https://raw.githubusercontent.com/ctllo-bit/FnDepot/main/fndepot.json')).toBe(ROOT); // FnDepot v1 索引
    expect(sourceKey('https://raw.githubusercontent.com/ctllo-bit/FnDepot/main/fndepot_v2.json')).toBe(ROOT); // FnDepot v2 索引
  });

  it('路径内 raw（github.com/u/r/raw/...）', () => {
    expect(sourceKey('https://github.com/ctllo-bit/FnDepot/raw/main/fnpack.json')).toBe(ROOT);
    expect(sourceKey('https://github.com/ctllo-bit/FnDepot/raw/master/fndepot_v2.json')).toBe(ROOT);
  });

  it('jsDelivr / gh-proxy 镜像形态', () => {
    expect(sourceKey('https://cdn.jsdelivr.net/gh/ctllo-bit/FnDepot/fnpack.json')).toBe(ROOT);
    expect(sourceKey('https://gh-proxy.com/https://raw.githubusercontent.com/ctllo-bit/FnDepot/main/fnpack.json')).toBe(ROOT);
    expect(sourceKey('https://mirror.ghproxy.com/https://github.com/ctllo-bit/FnDepot/raw/main/fnpack.json')).toBe(ROOT);
  });

  it('非 GitHub 主机：剥 /raw/<分支>/ 段与索引文件名', () => {
    expect(sourceKey('https://gitea.example.com/u/r/raw/branch/fnpack.json')).toBe('gitea.example.com/u/r');
    expect(sourceKey('http://nas.local:8080/fndepot.json')).toBe('nas.local:8080');
    expect(sourceKey('http://nas.local:8080/repo/fndepot_v2.json')).toBe('nas.local:8080/repo');
    expect(sourceKey('https://gitea.example.com/u/r')).toBe('gitea.example.com/u/r');
  });

  it('非链接形态 → null', () => {
    expect(sourceKey('')).toBe(null);
    expect(sourceKey('清理精灵')).toBe(null);
    expect(sourceKey('moo 1.0')).toBe(null);
  });
});

describe('isLinkLike 判定（0.6.198）', () => {
  it('链接形态为真', () => {
    expect(isLinkLike('https://github.com/a/b')).toBe(true);
    expect(isLinkLike('github.com/a/b')).toBe(true);
    expect(isLinkLike('github.com/a/b/raw/main/fnpack.json')).toBe(true);
    expect(isLinkLike('nas.local:8080/x.json')).toBe(true);
  });

  it('普通搜索词为假（防误伤）', () => {
    expect(isLinkLike('fnpack.json')).toBe(false); // 裸文件名
    expect(isLinkLike('fndepot_v2.json')).toBe(false);
    expect(isLinkLike('清理精灵')).toBe(false);
    expect(isLinkLike('mihomo v2')).toBe(false);
    expect(isLinkLike('')).toBe(false);
  });
});
