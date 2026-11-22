import React from 'react';
import type { AppInfo } from '../api/client';
import { AlertTriangle } from 'lucide-react';
import { cn } from '@/lib/utils';
// P1-4 已下架：详情页红条（头部与内容之间的唯一权威解释入口）
import { DELIST_BADGE, delistDetailNote, delistReasonShort } from '@/lib/delist';

/**
 * 已下架红条（DESIGN §5）。
 *
 * 命中 blacklist 的应用**不再 404**：详情正常打开，这里给出
 *   标题 = 「已下架 · <原因短句>」
 *   正文 = notice（≤60 字）+「Moo 已停止提供该应用的下载与更新；已安装的副本不会被远程卸载。」
 *   脚注 = 下架时间 + 来源
 *
 * 与视图无关：标准 / 极简 / 极光进入都是同一套白玻璃 + 红条
 *（下架应用在 AppDetailDialog 里强制忽略 aurora 底板）。
 */
interface DelistBannerProps {
  app: AppInfo;
  className?: string;
}

const DelistBanner: React.FC<DelistBannerProps> = ({ app, className }) => {
  if (!app.blacklisted) return null;
  return (
    <div
      data-testid="delist-banner"
      className={cn(
        'flex items-start gap-2.5 rounded-[14px] border border-destructive/35',
        'bg-destructive/[0.07] px-3.5 py-3 backdrop-blur-xl',
        className,
      )}
    >
      <span className="flex h-[30px] w-[30px] shrink-0 items-center justify-center rounded-[9px] border border-destructive/35 bg-destructive/10 text-destructive">
        <AlertTriangle className="h-[15px] w-[15px]" />
      </span>
      <div className="min-w-0">
        <div className="text-[13.5px] font-bold leading-tight text-destructive">
          {DELIST_BADGE} · {delistReasonShort(app.blacklist_reason)}
        </div>
        <div className="mt-1 text-xs leading-relaxed text-foreground/75">
          {delistDetailNote(app)}
        </div>
        <div className="mt-1.5 text-[11px] text-muted-foreground">
          {app.blacklist_since ? `下架时间 ${app.blacklist_since} · ` : ''}来源：远端下架黑名单
        </div>
      </div>
    </div>
  );
};

export default DelistBanner;
