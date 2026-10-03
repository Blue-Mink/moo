/**
 * 0.6.198 — 源链接归一化（搜索框贴源链接搜出该源全部应用）。
 *
 * 用户粘贴的"源链接"不止源列表里配置的仓库根链接，还有一堆浏览器可见
 * 形态：raw 前缀（raw.githubusercontent.com）、路径内 raw
 * （github.com/u/r/raw/...）、JSON 索引链接（**moo.json** / fnpack.json /
 * fndepot.json / fndepot_v2.json / apps.json）、jsDelivr / gh-proxy 镜像链接。全部归一化
 * 成同一规范 key（GitHub → `github.com/owner/repo`，小写、去 .git/分支/
 * 文件名）后才能对源列表做匹配。
 */

/** 源链接尾部可能挂的索引文件名（归一化时剥掉） */
const INDEX_FILES = ['moo.json', 'fnpack.json', 'fndepot_v2.json', 'fndepot.json', 'apps.json'];

/** GitHub 系主机（镜像前缀剥离 + 规范 key 派生用） */
const GH_HOSTS = [
  'github.com/',
  'raw.githubusercontent.com/',
  'cdn.jsdelivr.net/gh/',
];

/**
 * 词条是否像链接：带协议前缀，或 `域名(可选端口)/路径` 形态（host 必须含
 * 点 + 至少一个字母的 TLD，且点后必须还有 `/` 路径段——裸文件名
 * `fnpack.json` 不算链接，避免误伤普通搜索词）。
 */
export function isLinkLike(term: string): boolean {
  const t = term.trim().toLowerCase();
  if (!t) return false;
  if (/^https?:\/\//.test(t)) return true;
  // 域名形态，或 IPv4 主机（内网 Gitea 等：192.0.2.10:3000/owner/repo）
  return (
    /^[a-z0-9][a-z0-9.-]*\.[a-z]{2,}(:\d+)?\/\S*$/.test(t) ||
    /^(\d{1,3}\.){3}\d{1,3}(:\d+)?\/\S*$/.test(t)
  );
}

/**
 * 把源链接归一化成规范 key（小写）：
 *  - GitHub 系（含 raw 域 / jsDelivr / 镜像前缀）→ `github.com/owner/repo`
 *    （分支、文件、.git、/tree/... 一律剥掉）
 *  - 其他主机 → 剥协议、`/raw/<分支>/` 段、索引文件名后的 host+path
 * 返回 null = 非链接形态。
 */
export function sourceKey(raw: string): string | null {
  let u = raw.trim().toLowerCase();
  if (!u) return null;
  u = u.split(/[?#]/)[0]; // query / fragment
  u = u.replace(/^https?:\/\//, '');
  u = u.replace(/\/+$/, '');
  if (!u.includes('/')) return null; // 裸词/裸文件名/无路径主机 → 非链接
  const hostNoPort = (u.split('/')[0] || '').replace(/:\d+$/, '');
  // host 必须是域名或 IPv4（0.6.241：内网 Gitea 也是合法源主机）
  if (!/^[a-z0-9][a-z0-9.-]*\.[a-z]{2,}$/.test(hostNoPort) && !/^(\d{1,3}\.){3}\d{1,3}$/.test(hostNoPort)) return null;
  // 镜像前缀（gh-proxy 等）：剥掉首个已知 GitHub 主机之前的部分
  for (const h of GH_HOSTS) {
    const i = u.indexOf('/' + h);
    if (i >= 0) {
      u = u.slice(i + 1);
      break;
    }
  }
  let m: RegExpMatchArray | null;
  if ((m = u.match(/^raw\.githubusercontent\.com\/([^/]+)\/([^/]+)\/?/))) {
    return `github.com/${m[1]}/${m[2].replace(/\.git$/, '')}`;
  }
  if ((m = u.match(/^cdn\.jsdelivr\.net\/gh\/([^/]+)\/([^/]+)\/?/))) {
    return `github.com/${m[1]}/${m[2].replace(/\.git$/, '')}`;
  }
  if ((m = u.match(/^github\.com\/([^/]+)\/([^/]+?)(\/|$)/))) {
    return `github.com/${m[1]}/${m[2].replace(/\.git$/, '')}`;
  }
  // 非 GitHub 通用：剥 /raw/<分支>/ 段与索引文件名
  u = u.replace(/\/raw\/[^/]+\//, '/');
  for (const f of INDEX_FILES) {
    if (u.endsWith('/' + f)) {
      u = u.slice(0, -(f.length + 1));
      break;
    }
  }
  u = u.replace(/\/+$/, '');
  return u || null;
}
