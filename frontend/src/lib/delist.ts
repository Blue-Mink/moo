/**
 * P1-4 已下架（blacklist）文案与判定口径 —— 卡面 / 行列表 / 极简 / 极光 / 详情共用。
 *
 * 判定只看后端下发的 `blacklisted`（远端 blacklist.json 命中）；本模块只做
 * **展示**：原因码 → 短句、notice → 副行（缺省回落）。原因码永远不在卡面显示，
 * 未知码不瞎编，回落「已被停止分发」。
 *
 * 口径见 `_prep_p14_blacklist_badge/DESIGN.md` §2：
 *   - 角标恒为「已下架」（= 停止分发，不是已卸载）
 *   - 浮层标题 = reason 短句；副行 = notice（≤60 字），无则「moo 已停止提供下载与更新」
 */
import type { AppInfo } from '../api/client';

/** 角标文案（恒为这一句，不随原因码变化） */
export const DELIST_BADGE = '已下架';

/** 副行缺省文案（notice 为空时） */
export const DELIST_DEFAULT_NOTICE = 'moo 已停止提供下载与更新';

/** 卡面原因短句缺省（空 / 未知原因码） */
export const DELIST_DEFAULT_REASON = '已被停止分发';

/** 详情红条正文补充句（「为什么不能装 / 已装会不会被删」的唯一权威解释） */
export const DELIST_DETAIL_NOTE = 'Moo 已停止提供该应用的下载与更新；已安装的副本不会被远程卸载。';

const REASON_SHORT: Record<string, string> = {
  malware: '检测到恶意行为',
  fraud: '涉嫌欺诈或误导',
  copyright: '侵权投诉',
  security: '存在高危漏洞',
  policy: '违反社区规范',
  author: '应作者要求下架',
};

/** 该应用是否命中下架清单 */
export const isDelisted = (app?: Pick<AppInfo, 'blacklisted'> | null): boolean =>
  !!app?.blacklisted;

/** 原因码 → 卡面短句（≤10 字）；未知 / 空 → 「已被停止分发」 */
export const delistReasonShort = (code?: string | null): string =>
  REASON_SHORT[(code || '').trim().toLowerCase()] || DELIST_DEFAULT_REASON;

/** 副行：优先远端 notice，为空回落缺省句 */
export const delistNotice = (
  app?: Pick<AppInfo, 'blacklist_notice'> | null,
): string => (app?.blacklist_notice || '').trim() || DELIST_DEFAULT_NOTICE;

/** 详情红条正文：notice + 「Moo 已停止提供…」补充句（避免中文句号重复） */
export const delistDetailNote = (
  app?: Pick<AppInfo, 'blacklist_notice'> | null,
): string => {
  const notice = delistNotice(app);
  const sep = /[。！？.!?]$/.test(notice) ? ' ' : '。';
  return notice + sep + DELIST_DETAIL_NOTE;
};

/**
 * 详情页底板：下架应用**强制忽略 aurora**（标准白玻璃 + 红条，三视图进入同一套）。
 * 返回 '' 表示不套极光渐变。
 */
export const delistAuroraClass = (
  app: Pick<AppInfo, 'blacklisted' | 'appname' | 'display_name'> | null | undefined,
  aurora: boolean | undefined,
  pick: (name: string) => string,
): string => {
  if (!aurora || !app || app.blacklisted) return '';
  return pick(app.appname || app.display_name);
};

/**
 * 详情页主操作位语义：
 *   default = 走原有逻辑（未装=安装 / 已装=打开/启动）
 *   open    = 已装下架：保留「打开」（可继续用）
 *   blocked = 未装下架：不可点「已停止分发」，绝不出现安装按钮
 */
export type DelistPrimary = 'default' | 'open' | 'blocked';

export const delistPrimary = (app?: Pick<AppInfo, 'blacklisted' | 'installed'> | null): DelistPrimary => {
  if (!app?.blacklisted) return 'default';
  return app.installed ? 'open' : 'blocked';
};
