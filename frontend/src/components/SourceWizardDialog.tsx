import React, { useEffect, useState } from 'react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import type { WizardParam } from '@/api/client';

export interface SourceWizardField {
  key: string;
  label?: string;
  default?: string;
  required?: boolean;
}

interface SourceWizardDialogProps {
  /** 应用显示名（标题用）。 */
  appDisplayName: string;
  /** moo.json 声明的 wizard.fields（0.6.269 起实现）。 */
  fields: SourceWizardField[];
  onCancel: () => void;
  /** 提交时回传 [{key, value}]，与 FPK 向导参数同构（?wizard= 契约）。 */
  onConfirm: (params: WizardParam[]) => void;
}

/**
 * 源声明的安装向导（docs/MOO-PROTOCOL.md 示例 7）。
 *
 * 与 FPK 自带向导（WizardDialog，字段定义在包内 wizard/install）不同：
 * 这里的字段由**源作者在 moo.json 里声明**，键名由应用自身约定，
 * 收集值随安装请求的 ?wizard= 下发给安装管线。
 *
 * 单页平铺（字段数少）；必填项空值拦截提交（touched 后显示错误）。
 */
const SourceWizardDialog: React.FC<SourceWizardDialogProps> = ({
  appDisplayName,
  fields,
  onCancel,
  onConfirm,
}) => {
  const [values, setValues] = useState<Record<string, string>>({});
  const [touched, setTouched] = useState<Record<string, boolean>>({});

  useEffect(() => {
    const seed: Record<string, string> = {};
    for (const f of fields) {
      seed[f.key] = f.default ?? '';
    }
    setValues(seed);
    setTouched({});
  }, [fields]);

  const errorFor = (f: SourceWizardField): string | null => {
    if (f.required && (values[f.key] ?? '').trim() === '') {
      return '此项为必填';
    }
    return null;
  };

  const allValid = fields.every((f) => errorFor(f) === null);

  const confirm = () => {
    setTouched(Object.fromEntries(fields.map((f) => [f.key, true])));
    if (!allValid) return;
    onConfirm(fields.map((f) => ({ key: f.key, value: values[f.key] ?? '' })));
  };

  return (
    <Dialog open onOpenChange={(open) => !open && onCancel()}>
      <DialogContent className="rounded-[18px] max-w-[calc(100vw-1.5rem)] sm:max-w-md max-h-[calc(100dvh-2rem)] flex flex-col border-border/20 shadow-appstore bg-card">
        <DialogHeader className="shrink-0">
          <DialogTitle className="text-base font-semibold">
            安装参数 · {appDisplayName}
          </DialogTitle>
          <DialogDescription className="text-xs text-muted-foreground">
            该应用要求提供以下参数（键名由应用定义），安装后在应用自身设置中调整。
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-3 overflow-y-auto px-1 py-1 min-h-0 flex-1">
          {fields.map((f) => {
            const err = errorFor(f);
            return (
              <div key={f.key} className="space-y-1">
                <label className="flex items-center gap-1.5 text-[13px] font-medium">
                  {f.label || f.key}
                  {f.required && (
                    <span className="text-destructive text-xs" aria-hidden>
                      *
                    </span>
                  )}
                  <code className="text-[11px] font-mono text-muted-foreground/70">
                    {f.key}
                  </code>
                </label>
                <Input
                  value={values[f.key] ?? ''}
                  placeholder={f.default ? '' : f.key}
                  onChange={(e) =>
                    setValues((prev) => ({ ...prev, [f.key]: e.target.value }))
                  }
                  onBlur={() =>
                    setTouched((prev) => ({ ...prev, [f.key]: true }))
                  }
                  className="h-9 text-sm"
                />
                {touched[f.key] && err && (
                  <p className="text-xs text-destructive">{err}</p>
                )}
              </div>
            );
          })}
        </div>

        <DialogFooter className="shrink-0 gap-2 pt-3">
          <Button variant="outline" onClick={onCancel} className="h-9 text-sm">
            取消
          </Button>
          <Button onClick={confirm} className="h-9 text-sm">
            开始安装
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
};

export default SourceWizardDialog;
