import React from 'react';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';

export interface UpdateBlocker {
  appname: string;
  display_name?: string;
  status?: string;
}

interface UpdateBlockedDialogProps {
  appName: string;
  /** 后端 error.detail.kind */
  kind: 'dependency_blocked' | 'license_required';
  /** dependency_blocked：正在占用目标应用、需要先停用的依赖方 */
  blockers?: UpdateBlocker[];
  version?: string;
  busy?: boolean;
  onClose: () => void;
  /** dependency_blocked：用户确认「停用并重试」 */
  onStopAndRetry?: () => void;
}

/**
 * 0.6.5xx：官方 cloud 升级的两类「需要人做一步」的失败。
 *
 * 此前只会弹一句 toast（「正被 X 使用，请先停用后再升级」/「需要先接受许可协议」），
 * 用户得自己去应用中心翻出是哪个应用、在哪个页面同意，链路断在这里。
 * 现在：依赖占用直接列出来并给「停用并重试」；许可协议给明确指引（平台要求
 * 交互同意，Moo 已带 agreedToProtocol=true 仍被拒，不能替用户点）。
 */
const UpdateBlockedDialog: React.FC<UpdateBlockedDialogProps> = ({
  appName,
  kind,
  blockers,
  version,
  busy,
  onClose,
  onStopAndRetry,
}) => {
  const isDep = kind === 'dependency_blocked';
  const list = blockers ?? [];
  return (
    <Dialog open onOpenChange={(open) => !open && !busy && onClose()}>
      <DialogContent className="rounded-[18px] max-w-[calc(100vw-1.5rem)] sm:max-w-md border-border/20 shadow-appstore bg-card">
        <DialogHeader>
          <DialogTitle>{isDep ? `先停用依赖方再升级 ${appName}` : `需要同意许可协议`}</DialogTitle>
          <DialogDescription className="text-xs leading-relaxed">
            {isDep
              ? '以下应用正在使用它，平台不允许覆盖正在被使用的运行时目录：'
              : `${appName}${version ? ` v${version}` : ''} 需要先在飞牛应用中心同意许可协议，完成后回到这里重新点更新。`}
          </DialogDescription>
        </DialogHeader>

        {isDep && (
          <ul className="space-y-1.5 px-1 py-1">
            {list.map((b) => (
              <li
                key={b.appname}
                className="flex items-center justify-between gap-3 rounded-lg border border-border/40 bg-muted/20 px-3 py-2"
              >
                <span className="text-sm">{b.display_name || b.appname}</span>
                <span className="text-[11px] text-muted-foreground tabular-nums">
                  {b.status || '运行中'}
                </span>
              </li>
            ))}
            {list.length === 0 && (
              <li className="text-xs text-muted-foreground px-1">（平台未返回具体应用）</li>
            )}
          </ul>
        )}

        <DialogFooter>
          <div className="flex w-full items-center justify-between gap-2">
            <Button variant="outline" onClick={onClose} disabled={busy}>
              {isDep ? '取消' : '知道了'}
            </Button>
            {isDep && onStopAndRetry && (
              <Button onClick={onStopAndRetry} disabled={busy || list.length === 0}>
                {busy ? '停用中…' : '停用并重试'}
              </Button>
            )}
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
};

export default UpdateBlockedDialog;
