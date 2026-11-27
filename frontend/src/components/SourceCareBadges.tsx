/**
 * 应用源「自动监测」状态徽标（2026-11-26 CARE P1-2 / P1-1c）。
 *
 * 用户令（BACKLOG §B-SRC-CARE-DEADFEED 📱 窄屏显示约束）：
 *  - 宽屏（≥sm）维持 inline 角标（应用数胶囊之后），位置不变；
 *  - 窄屏（<sm，含 390px）下沉到 URL 行下的第二行 text-[11px]，不占源名横向
 *    空间（现角标 max-w-[8.5rem] + 错误徽章会把源名压成「galax…」）；
 *  - **文案必须分因**：失败态「连续 N 次刷新失败 · 原因」，空源态「连续 N 次
 *    无应用 · 再 M 次自动关闭」，禁止两者都写「无应用」；
 *  - 原因界面截 ≈40 字，全文放 title；
 *  - 禁止 absolute 贴右上（右侧是复制/刷新/开关/折叠/拖拽手柄，会撞）。
 *
 * 2026-11-26 CARE P1-1b 追加两因（优先级在 fail/empty 之前）：
 *  - heal-capped：disabled_by=auto-care 且 30 天自动恢复达上限 → 保持停用等人工确认；
 *  - recovered：24h 内刚自动恢复 → 也走同一 inline + 第二行结构（不加层级）。
 */
import { Badge } from '@/components/ui/badge';
import type { SourceEntry } from '../api/client';
import { careAgo } from '../lib/careTime';

/** 与服务端 AutoCareThreshold（5）对齐——仅用于文案「再 N 次」，不改门控。 */
const CARE_THRESHOLD = 5;
/** 原因在界面上截断的字符数（title 里仍是全文）。 */
const REASON_MAX = 40;
/** heal_count_30d 缺省值（服务端口径：30 天内最多自动恢复 2 次）。 */
const HEAL_CAP_DEFAULT = 2;

export interface CareHint {
  tone: 'fail' | 'empty' | 'auto-off' | 'heal-capped' | 'recovered';
  /** 宽屏 inline 胶囊文案（短） */
  inline: string;
  /** 窄屏第二行文案（分因 + 原因 / 剩余轮次） */
  second: string;
  /** title 全文（不截断） */
  title: string;
}

/** 色调 → 文字色（结构/层级不变，只换颜色；不改布局类）。 */
const TONE_TEXT: Record<CareHint['tone'], string> = {
  fail: 'text-amber-500',
  empty: 'text-amber-500',
  'auto-off': 'text-amber-500',
  // 需人工确认：比告警更重，用红色
  'heal-capped': 'text-red-500',
  // 已恢复：正向反馈，用绿色
  recovered: 'text-emerald-600',
};

const clip = (s: string, max: number): string => {
  const chars = Array.from(s.trim());
  return chars.length <= max ? chars.join('') : chars.slice(0, max).join('') + '…';
};

/** 纯函数：由源条目推导出「该显示什么」。无状态 → 可直接单测。
 *  now 可注入（默认真实时钟），只影响「X 分钟前」这类相对时间，便于测试稳定。 */
export function careHint(src: SourceEntry, now: number = Date.now()): CareHint | null {
  const fail = src.fail_streak ?? 0;
  const empty = src.empty_streak ?? 0;
  if (src.enabled === false) {
    // 策略停用才提示「可自动恢复」；用户手动关闭保持原「已关闭」语义。
    if (src.disabled_by !== 'auto-care') return null;
    // 30 天自愈已达上限：服务端不再自动恢复，明确要人工确认（优先级高于普通停用）
    if (src.heal_capped) {
      const n = src.heal_count_30d ?? HEAL_CAP_DEFAULT;
      return {
        tone: 'heal-capped',
        inline: '已自动停用',
        second: `⚠ 已自动停用 · 30 天内自动恢复已达 ${n} 次，请手动确认`,
        title: `已自动恢复 ${n} 次仍未稳定，Moo 已停止自动恢复，每 24 小时只做一次只读复查；请在应用源页确认源是否仍有效后手动开启`,
      };
    }
    const reason = src.last_error || '源地址已失效';
    return {
      tone: 'auto-off',
      inline: '已自动停用',
      second: `⚠ 已自动停用 · ${clip(reason, REASON_MAX)}`,
      title: `源连续刷新失败已被自动停用（${reason}）；可在右侧开关重新启用，Moo 每 6 小时会低频复查，连续 2 次看到有应用会自动恢复`,
    };
  }
  // 24h 内刚自动恢复：正向反馈（仍走 inline + 第二行，不加层级 / 不用 absolute）。
  // 仅在「当前无失败计数」时展示——否则会把正在发生的失败盖成「已恢复」，
  // 与「文案必须分因、不能误判」冲突（宁可显示失败，不可错报已恢复）。
  // 空源计数同样不能盖住：恢复了但马上又空，正在发生的信号必须优先展示。
  if (fail === 0 && empty === 0) {
    const ago = careAgo(src.last_recovered_at, now);
    if (ago) {
      const apps = src.app_count ?? 0;
      return {
        tone: 'recovered',
        inline: '已自动恢复',
        second: `✓ 已于 ${ago}自动恢复 · 现有 ${apps} 个应用`,
        title: '此前因自动监测被停用，已自动恢复；如再次异常会重新进入监测',
      };
    }
  }
  if (fail > 0) {
    const reason = src.last_error || '刷新失败';
    return {
      tone: 'fail',
      inline: `连续 ${fail} 次刷新失败`,
      second: `⚠ 连续 ${fail} 次刷新失败 · ${clip(reason, REASON_MAX)}`,
      title: `连续 ${fail} 次刷新失败；再 ${Math.max(0, CARE_THRESHOLD - fail)} 次将自动停用。原因：${reason}`,
    };
  }
  if (empty > 0) {
    return {
      tone: 'empty',
      inline: `连续 ${empty} 次无应用`,
      second: `⚠ 连续 ${empty} 次无应用 · 再 ${Math.max(0, CARE_THRESHOLD - empty)} 次自动关闭`,
      title: `连续 ${empty} 次刷新成功但没有应用；再 ${Math.max(0, CARE_THRESHOLD - empty)} 次将自动关闭（可在应用源页重新启用）`,
    };
  }
  return null;
}

/** 宽屏 inline 角标（窄屏隐藏，改由第二行承载）。 */
export function SourceCareInline({ src, now }: { src: SourceEntry; now?: number }) {
  const hint = careHint(src, now);
  if (!hint) return null;
  return (
    <Badge
      variant="secondary"
      data-testid="care-inline"
      data-care-tone={hint.tone}
      title={hint.title}
      className={`hidden h-5 max-w-[8.5rem] min-w-0 shrink-0 truncate whitespace-nowrap px-1.5 text-[10px] font-normal ${TONE_TEXT[hint.tone]} sm:inline-flex`}
    >
      {hint.inline}
    </Badge>
  );
}

/** 窄屏第二行（<sm 显示）：分因小字，禁 absolute、不挤压源名与应用数胶囊。 */
export function SourceCareSecondLine({ src, now }: { src: SourceEntry; now?: number }) {
  const hint = careHint(src, now);
  if (!hint) return null;
  return (
    <div
      data-testid="care-line"
      data-care-tone={hint.tone}
      title={hint.title}
      className={`mt-0.5 break-words text-[11px] leading-4 ${TONE_TEXT[hint.tone]} sm:hidden`}
    >
      {hint.second}
    </div>
  );
}
