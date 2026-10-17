// 0.6.326z5 推荐位禁用漏判修复（前端 2.1）——纯逻辑抽取。
//
// 背景：`user_hidden`（用户手动禁用）的卡片在「全部/发现/有更新/搜索/分类」
// 列表里已被 `filteredApps` 排除，但推荐位（固定前三）这条路径此前会把它
// 重新钉回列表：
//   · `App.tsx displayApps` 的钉序 `find` 取自**未过滤**的 `apps`；
//   · `FeaturedShowcase` 的 `featuredProp` 分支直接 `slice(0,3)`，无过滤。
// 后端 `recommended` 的随机补足池已排 Hidden/UserHidden，故只有「固定前三」
// 这条路漏判——前端两处一并修掉，逻辑抽到本模块以便单测覆盖矩阵。
//
// 语义边界（刻意保持，勿改）：「已安装」tab 仍保留被禁用卡（见 App.tsx 的
// `filteredApps`），计数口径不变；本模块只管推荐钉位的去留。
import type { AppInfo } from '../api/client';

/** 卡片 base key = `appname@源名` 去掉 `@源名` 后的 appname；无 key/appname 时空串。 */
export function baseKeyOf(a: Pick<AppInfo, 'key' | 'appname'> | null | undefined): string {
  return ((a?.key || a?.appname || '') as string).split('@')[0];
}

/** 判定一张卡是否「用户手动禁用」（列表隐藏态）。 */
export function isHiddenApp(a: Pick<AppInfo, 'user_hidden'> | null | undefined): boolean {
  return a?.user_hidden === true;
}

export interface PinnedRecommended {
  /** 钉在列表最前的推荐卡（按 recommendedApps 原序；命中禁用的位整位跳过）。 */
  pinned: AppInfo[];
  /**
   * 已占位的 base key 集合（**含被跳过的那一位**）——调用方据此从其余列表里
   * 剔除同 base 的卡，否则被跳过的禁用卡会从 rest 里冒出来。
   */
  baseKeys: Set<string>;
}

/**
 * 解析发现页固定前三推荐位（0.6.314r 用户定稿：顺序由后端 /api/recommended 决定）。
 *
 * 行为契约（逐项等价于抽取前 App.tsx:1452-1470 的内联实现）：
 *  · 逐个 `recommendedApps.slice(0, 3)`，用 base key 在 `apps` 里找同 appname 的
 *    真实卡片；找不到则回落推荐条目本身（源缺失时后端给 AppInfo 同构快照）。
 *  · 命中卡 `user_hidden` → **该位跳过**（不 pin）；但 base 仍记入 `baseKeys`。
 *  · 不足 3 位不再补足（由调用方的 rest 逻辑自然续上，顺序/去重语义不变）。
 */
export function resolvePinnedRecommended(
  apps: AppInfo[],
  recommendedApps: AppInfo[],
): PinnedRecommended {
  const pinned: AppInfo[] = [];
  const baseKeys = new Set<string>();
  for (const rec of recommendedApps.slice(0, 3)) {
    const base = baseKeyOf(rec);
    const hit = base ? apps.find(a => baseKeyOf(a) === base) : undefined;
    if (base) baseKeys.add(base);
    if (isHiddenApp(hit)) continue; // 2.1：命中禁用卡整位跳过（base 已记，防从 rest 冒出）
    pinned.push(hit ?? rec);
  }
  return { pinned, baseKeys };
}

/**
 * 发现页横幅前三（FeaturedShowcase）。
 *
 * 行为契约：
 *  · `featuredProp` 非空 → **先过滤 user_hidden 再 slice(0, 3)**；
 *  · 过滤后不足 3 位 → 回落既有随机补足逻辑（池 = `apps` 中非禁用、且 base 未占位的卡；
 *    splice 抽取天然不重复）；
 *  · `featuredProp` 缺省/为空 → 与旧行为完全一致（全目录非禁用随机 3 款）。
 */
export function pickFeaturedApps(apps: AppInfo[], featuredProp?: AppInfo[]): AppInfo[] {
  const pinned = (featuredProp ?? []).filter(a => !isHiddenApp(a)).slice(0, 3);
  const out: AppInfo[] = [...pinned];
  if (out.length >= 3) return out;
  const taken = new Set(pinned.map(baseKeyOf).filter(Boolean));
  const pool = apps.filter(a => !isHiddenApp(a) && !taken.has(baseKeyOf(a)));
  while (out.length < 3 && pool.length > 0) {
    const j = Math.floor(Math.random() * pool.length);
    out.push(pool.splice(j, 1)[0]);
  }
  return out;
}

export interface DisplayAppsInput {
  /** 全量目录（未过滤）——钉位取真实卡片用。 */
  apps: AppInfo[];
  /** 已按 tab/搜索/分类过滤后的列表（推荐页已排 user_hidden）。 */
  filteredApps: AppInfo[];
  recommendedApps: AppInfo[];
  activeFilter: string;
  searchQuery: string;
  activeCategory?: string | null;
}

/**
 * 发现页列表装配：固定前三钉序 + 其余列表同 base 去重。
 *
 * 门控（逐项等价于抽取前 App.tsx:1452 的首行判断）：仅发现页、推荐位满 3 条、
 * 无搜索词（trim 后为空）、无分类过滤时才钉序；否则原样返回 `filteredApps`
 * （**返回同一引用**，调用方 memo 语义不变）。
 */
export function resolveDisplayApps(input: DisplayAppsInput): AppInfo[] {
  const { apps, filteredApps, recommendedApps, activeFilter, searchQuery, activeCategory } = input;
  if (activeFilter !== 'recommended' || recommendedApps.length < 3 || searchQuery.trim() || activeCategory) {
    return filteredApps;
  }
  const { pinned, baseKeys } = resolvePinnedRecommended(apps, recommendedApps);
  const rest = filteredApps.filter(a => {
    const b = baseKeyOf(a);
    return !b || !baseKeys.has(b);
  });
  return [...pinned, ...rest];
}
