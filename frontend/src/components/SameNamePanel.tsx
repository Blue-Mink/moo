/**
 * D3（0.6.579）同名透明化 · **只提示不合并**
 *
 * 两类提示，全部是「已算好的展示字段」，不触发任何动作、不改任何判定：
 *   1. 大小写同名变体：`same_name_ci_count` > 0 ⇒ 列表卡角标「另有同名变体」，
 *      详情页出候选表（`same_name` ∪ `same_name_ci`，两者互补不重复）；
 *   2. 原项目源（D2）：代表卡是「已装 + 搬运」而同组还有真实项目卡
 *      （`upstream_repo == ""`）时，明确写出「原项目源：<source> v<ver>」。
 *
 * 兼容口径：旧后端缺 `same_name_ci*` ⇒ 全部函数返回「无」，界面零变化。
 */
import React, { useState } from 'react';
import type { AppInfo, SameNameEntry } from '../api/client';
import { Badge } from '@/components/ui/badge';
import { Separator } from '@/components/ui/separator';
import { ChevronDown, ChevronRight, Copy } from 'lucide-react';

/** 列表卡角标文案（与后端 same_name_ci_count 语义一致）。 */
export const CI_BADGE = '另有同名变体';

/** 有大小写同名变体？旧后端缺字段 ⇒ false（静默）。 */
export const hasCaseVariant = (app?: AppInfo | null): boolean => (app?.same_name_ci_count ?? 0) > 0;

/** 变体卡数（角标 title 用；缺字段 ⇒ 0）。 */
export const caseVariantCount = (app?: AppInfo | null): number => app?.same_name_ci_count ?? 0;

/**
 * 合并 `same_name`（字面完全相同组）+ `same_name_ci`（字面不同的变体），
 * 按 key 去重、精确同名组在前。缺字段 ⇒ 空数组。
 */
export function sameNameCandidates(app?: AppInfo | null): SameNameEntry[] {
  const out: SameNameEntry[] = [];
  const seen = new Set<string>();
  const push = (list?: SameNameEntry[]) => {
    for (const e of list ?? []) {
      if (!e) continue;
      const k = e.key || `${e.source || ''}@${e.version || ''}`;
      if (seen.has(k)) continue;
      seen.add(k);
      out.push(e);
    }
  };
  push(app?.same_name);
  push(app?.same_name_ci);
  return out;
}

/**
 * D2 反例提示（_audit_0578/BATCH3_D2_AUDIT.md §4.2 的三组）：
 * 当前卡 = 已装 + 搬运（`upstream_repo` 非空），且**精确同名组**里存在
 * `upstream_repo == ""` 的真实项目卡（通常被「已装卡优先」隐藏）。
 * 返回该真实卡条目；不满足 ⇒ null（旧后端/普通组都走这条）。
 */
export function originalSourceOf(app?: AppInfo | null): SameNameEntry | null {
  if (!app || !app.installed || !app.upstream_repo) return null;
  for (const e of app.same_name ?? []) {
    if (!e || e.installed) continue;
    if (e.upstream_repo) continue; // 搬运卡不是「原项目源」
    return e;
  }
  return null;
}

/** 候选表里的一枚小标签（统一中性样式，不抢主信息）。 */
const Chip: React.FC<{ children: React.ReactNode; tone?: 'primary' | 'muted' | 'warn' }> = ({ children, tone = 'muted' }) => (
  <span
    className={
      'inline-flex max-w-full items-center rounded-full border px-1.5 py-[1px] text-[11px] leading-[16px] ' +
      (tone === 'primary'
        ? 'border-primary/35 bg-primary/10 text-primary'
        : tone === 'warn'
          ? 'border-amber-500/35 bg-amber-500/10 text-amber-700 dark:text-amber-400'
          : 'border-border/40 bg-muted/50 text-muted-foreground')
    }
  >
    <span className="truncate">{children}</span>
  </span>
);

/**
 * 详情页「同名卡片」区（默认折叠，点标题展开 —— 详情页信息密度高，
 * 候选表只在用户关心「为什么有两张」时才展开）。
 * 无同名兄弟卡且无 D2 提示 ⇒ 返回 null（普通应用零变化）。
 */
export const SameNamePanel: React.FC<{
  app?: AppInfo | null;
  /** 点候选行 → 打开该卡详情（沿用既有详情通道，不新造安装通道）。 */
  onOpenKey?: (key: string) => void;
}> = ({ app, onOpenKey }) => {
  const cands = sameNameCandidates(app);
  const hint = originalSourceOf(app);
  const ciCount = caseVariantCount(app);
  const [open, setOpen] = useState(false);

  // 只有一张卡（自己）且无 D2 提示 ⇒ 什么都不渲染
  if (cands.length < 2 && !hint) return null;

  const installedVer = app?.installed_version || app?.latest_version || '';
  return (
    <>
      <Separator />
      <div data-testid="same-name-panel" className="space-y-1.5">
        <button
          type="button"
          data-testid="same-name-toggle"
          aria-expanded={open}
          onClick={() => setOpen(v => !v)}
          className="flex w-full min-w-0 items-center gap-1.5 text-xs text-muted-foreground transition-colors hover:text-foreground"
        >
          {open ? <ChevronDown className="h-3.5 w-3.5 shrink-0" /> : <ChevronRight className="h-3.5 w-3.5 shrink-0" />}
          <Copy className="h-3.5 w-3.5 shrink-0" />
          <span className="truncate">同名卡片 · {cands.length} 个条目</span>
          {ciCount > 0 && (
            <Badge
              variant="secondary"
              className="ml-1 h-5 shrink-0 rounded-full border-0 bg-muted px-1.5 text-[10px] font-normal text-muted-foreground"
            >
              {CI_BADGE} {ciCount}
            </Badge>
          )}
        </button>

        {hint && (
          <p
            data-testid="original-source-hint"
            className="rounded-lg border border-amber-500/30 bg-amber-500/10 px-2.5 py-1.5 text-xs leading-relaxed text-amber-700 dark:text-amber-400"
          >
            原项目源：<span className="font-semibold">{hint.source || '—'}</span>
            {hint.version ? ` v${hint.version}` : ''}
            （当前列表展示的是已装卡{installedVer ? ` v${installedVer}` : ''}）
          </p>
        )}

        {open && (
          <ul
            data-testid="same-name-list"
            className="divide-y divide-border/40 overflow-hidden rounded-xl border border-border/50"
          >
            {cands.map(e => (
              <li key={e.key || `${e.source}@${e.version}`} className="min-w-0">
                <button
                  type="button"
                  data-testid="same-name-row"
                  data-key={e.key}
                  onClick={() => e.key && onOpenKey?.(e.key)}
                  className="flex w-full min-w-0 flex-col gap-0.5 px-3 py-2 text-left transition-colors hover:bg-muted/40"
                >
                  <span className="flex min-w-0 items-baseline gap-1.5">
                    <span className="truncate text-sm font-medium text-foreground/90">{e.source || '—'}</span>
                    <span className="shrink-0 text-xs text-muted-foreground">{e.version ? `v${e.version}` : '—'}</span>
                    {e.arch && <span className="shrink-0 text-xs text-muted-foreground/70">{e.arch}</span>}
                  </span>
                  <span className="flex flex-wrap items-center gap-1">
                    {e.is_rep && <Chip tone="primary">列表展示</Chip>}
                    {e.case_variant && <Chip tone="warn">大小写变体</Chip>}
                    {e.installed && <Chip>已装</Chip>}
                    {e.source_is_fork && <Chip>fork 源</Chip>}
                    {e.is_upstream && <Chip>上游</Chip>}
                    {e.upstream_repo ? <Chip>搬运 {e.upstream_repo}</Chip> : <Chip>原项目源</Chip>}
                  </span>
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
    </>
  );
};

export default SameNamePanel;
