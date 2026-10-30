// 0.6.314r2（用户 10-08 定稿）：更新日志内容分页显示——
// 更新药丸弹窗 + 关于页「最新更新日志」卡共用本组件（两处原为 ReadmeRender+滚动盒）。
// 粒度=按行（空行忽略，一行一条）：
//  - ≤5 行 = 单页，直接渲染原文（与旧行为逐字节一致，短日志零视觉变化，无分页条）
//  - >5 行 = 每页 5 行，底部「‹ 1/3 ›」分页条；换文本回第 1 页
// 每页仍走 ReadmeRender（README 同款渲染；maxH 兜底防单行超长）
import React, { useEffect, useMemo, useState } from 'react';
import { ChevronLeft, ChevronRight } from 'lucide-react';
import { ReadmeRender } from './AppDetailDialog';

const PER_PAGE = 5;

const splitEntries = (text: string): string[] =>
  (text || '')
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean);

export const ChangelogPager: React.FC<{ text: string; appKey?: string; maxH?: string }> = ({
  text,
  appKey = 'moo',
  maxH = 'max-h-[110px]',
}) => {
  const entries = useMemo(() => splitEntries(text), [text]);
  const pages = Math.max(1, Math.ceil(entries.length / PER_PAGE));
  const [page, setPage] = useState(0);
  // 换文本（版本切换/探测刷新）回第 1 页
  useEffect(() => {
    setPage(0);
  }, [text]);

  if (!entries.length) return null;

  // 0.6.326（2026-10-17 用户令）：更新日志**按行显示**——markdown 的软换行默认被折叠成空格
  //（所以多条日志会挤成一段）。这里把每个条目之间补一个空行（markdown 空行 =
  // 独立段落，必然分行）。
  // ⚠️ 段间距必须用 `!important` 压掉：ReadmeRender 自带 `[&_p]:my-2`（prose 段距），
  // 两个 arbitrary variant 的 specificity 相同、由生成顺序决定胜负 → 实测 my-0 被覆盖，
  // 每行之间留下 8px 空隙（用户 2026-10-17 明确「中间不要有空行」）。
  // ⚠️ Tailwind 4.3 的 important 必须写**尾缀**：`[margin:0]!` → `margin:0 !important`；
  // 写成 `[margin:0!important]` 会被丢弃（实测 CSS 里 0 命中，构建门已拦过一次）。
  //「版本更新弹窗」与「关于页·最新更新日志」两处共用本组件，同时生效。
  const body = (t: string) => (
    <div className="[&_p]:[margin:0]!">
      <ReadmeRender readme={t} appKey={appKey} maxH={maxH} />
    </div>
  );
  const perLine = (lines: string[]) => lines.join('\n\n');

  // 单页=原文直渲（保留原始空行/换行的 markdown 排版）
  if (pages === 1) {
    return body(perLine(entries));
  }

  const cur = Math.min(page, pages - 1);
  const slice = perLine(entries.slice(cur * PER_PAGE, (cur + 1) * PER_PAGE));
  return (
    <div className="space-y-1.5">
      {body(slice)}
      <div className="flex items-center justify-center gap-2 pt-0.5">
        <button
          type="button"
          onClick={() => setPage((p) => Math.max(0, p - 1))}
          disabled={cur === 0}
          aria-label="上一页"
          title="上一页"
          className="flex h-6 w-6 items-center justify-center rounded-full border border-white/10 bg-card/55 text-muted-foreground transition-colors hover:bg-card/80 disabled:opacity-40"
        >
          <ChevronLeft className="h-3.5 w-3.5" />
        </button>
        <span className="text-[11px] tabular-nums text-muted-foreground">
          {cur + 1}/{pages}
        </span>
        <button
          type="button"
          onClick={() => setPage((p) => Math.min(pages - 1, p + 1))}
          disabled={cur >= pages - 1}
          aria-label="下一页"
          title="下一页"
          className="flex h-6 w-6 items-center justify-center rounded-full border border-white/10 bg-card/55 text-muted-foreground transition-colors hover:bg-card/80 disabled:opacity-40"
        >
          <ChevronRight className="h-3.5 w-3.5" />
        </button>
      </div>
    </div>
  );
};
