import React from 'react';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Progress } from "@/components/ui/progress"
import { Button } from "@/components/ui/button"
import { formatSpeed, formatProgress } from "@/lib/utils"

interface ProgressOverlayProps {
  visible: boolean;
  message: string;
  progress: number;
  speed?: number;
  downloaded?: number;
  total?: number;
  onCancel?: () => void;
  onDismiss?: () => void;
}

const ProgressOverlay: React.FC<ProgressOverlayProps> = ({ visible, message, progress, speed, downloaded, total, onCancel, onDismiss }) => {
  // 0.6.325 H4(D2) 双保险：后端已用 platform.NormalizeProgress 归一，
  // 显示端再钳制 0-100，防任何链路漏网时出现 >100% / 负数进度。
  const pct = Math.min(100, Math.max(0, progress));
  return (
    <Dialog open={visible} onOpenChange={() => {}}>
      <DialogContent 
        // 与其他 App Store 卡片统一：18px 圆角 + 卡片边框/阴影（sm:rounded-[18px] 来自 ui/dialog 基础类）
        className="sm:max-w-sm rounded-[18px] border-border/20 shadow-appstore bg-card [&>button]:hidden" 
        onInteractOutside={(e) => e.preventDefault()}
        onEscapeKeyDown={(e) => e.preventDefault()}
      >
        <DialogHeader>
          <DialogTitle>正在处理...</DialogTitle>
        </DialogHeader>
        
        <div className="flex flex-col gap-2 py-2">
          <div className="flex justify-between text-sm text-muted-foreground">
            <span>{message}</span>
            <span>{Math.round(pct)}%</span>
          </div>
          <Progress value={pct} className="w-full" />
          {speed != null && speed > 0 && (
            <div className="flex justify-between text-xs text-muted-foreground">
              <span>{formatSpeed(speed)}</span>
              {downloaded != null && total != null && total > 0 && (
                <span>{formatProgress(downloaded, total)}</span>
              )}
            </div>
          )}
        </div>

        {(onCancel || onDismiss) && (
          <div className="flex justify-end pt-2">
            {onCancel ? (
              <Button variant="ghost" size="sm" onClick={onCancel}>
                取消
              </Button>
            ) : (
              <Button variant="ghost" size="sm" onClick={onDismiss}>
                关闭
              </Button>
            )}
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
};

export default ProgressOverlay;
