import React, { useState, useEffect, useCallback } from 'react';
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import {
  Loader2, ChevronLeft, CheckCircle2, XCircle, KeyRound, LogOut,
  ShieldCheck, ExternalLink,
} from 'lucide-react';
import {
  fetchOfficialStatus, officialAuthorize, officialCallback, officialCancel,
  officialLogout, officialAuthorizeHeadless, fetchOfficialApps, type OfficialStatus,
} from '../api/client';
import { toast } from 'sonner';

// 官方应用中心 OAuth 免登录连接（0.6.253）。
//
// 流程：生成 PKCE 授权链接 → 授权页（面板 /signin）在本对话框 iframe 内
// 直接打开（免切浏览器，/signin 无 X-Frame-Options 可嵌入，10-03 实测）→
// 页面内登录（2FA 由面板页面自己处理）→ 页面显示一次性验证码 →
// 点左上返回按钮（同应用详情页样式）切回本对话框 → 粘贴验证码完成授权。
// 授权成功后官方目录/详情走 OAuth token（1h 自动刷新），不再触发面板登录。

interface OfficialOAuthDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** 连接状态变化后通知父级刷新目录 */
  onCatalogChanged?: () => void;
}

type View = 'status' | 'iframe' | 'code';

// 推导用户浏览器可达的面板地址：Moo UI 与面板同主机；
// https 入口对应面板 https 端口 5667，http 对应 5666。
const guessPanelBase = () => {
  const host = window.location.hostname || 'localhost';
  return window.location.protocol === 'https:'
    ? `https://${host}:5667`
    : `http://${host}:5666`;
};

export const OfficialOAuthDialog: React.FC<OfficialOAuthDialogProps> = ({
  open, onOpenChange, onCatalogChanged,
}) => {
  const [view, setView] = useState<View>('status');
  const [status, setStatus] = useState<OfficialStatus | null>(null);
  const [base, setBase] = useState(guessPanelBase);
  const [authUrl, setAuthUrl] = useState('');
  const [code, setCode] = useState('');
  const [busy, setBusy] = useState(false);
  // 0.6.255：无头授权的临时面板账号（旧版 fnOS 前端无授权 UI 时用；不保存）
  const [hhUser, setHhUser] = useState('');
  const [hhPass, setHhPass] = useState('');

  const refreshStatus = useCallback(async () => {
    try {
      setStatus(await fetchOfficialStatus());
    } catch {
      setStatus(null);
    }
  }, []);

  useEffect(() => {
    if (open) {
      setView('status');
      setCode('');
      setBusy(false);
      void refreshStatus();
    }
  }, [open, refreshStatus]);

  // ── 发起授权（生成链接 → 切 iframe 视图） ──
  const startAuthorize = useCallback(async () => {
    setBusy(true);
    try {
      const { url } = await officialAuthorize(base.trim() || undefined);
      setAuthUrl(url);
      setView('iframe');
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '发起授权失败');
    } finally {
      setBusy(false);
    }
  }, [base]);

  // ── 提交验证码 → 换 token → 拉目录验证 ──
  const submitCode = useCallback(async () => {
    const c = code.trim();
    if (!c) return;
    setBusy(true);
    try {
      await officialCallback(c);
      // 验证：拉官方全量目录（证明 token 可用）
      const { total } = await fetchOfficialApps();
      toast.success(`连接成功，官方目录共 ${total} 个应用`);
      setCode('');
      await refreshStatus();
      setView('status');
      onCatalogChanged?.();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '验证码无效或已过期（5 分钟内有效，请重新授权）');
    } finally {
      setBusy(false);
    }
  }, [code, refreshStatus, onCatalogChanged]);

  const cancelAuth = useCallback(async () => {
    try { await officialCancel(); } catch { /* 忽略 */ }
    setView('status');
  }, []);

  // 0.6.255：无头授权（旧版 fnOS 前端无授权 UI 时的替代路径；
  // 临时面板账号仅本次使用，不落地）
  const headlessAuth = useCallback(async () => {
    if (!hhUser.trim() || !hhPass) {
      toast.error('请填写本机 Web 面板的账号与密码（仅本次授权使用，不保存）');
      return;
    }
    setBusy(true);
    try {
      await officialAuthorizeHeadless(hhUser.trim(), hhPass);
      const { total } = await fetchOfficialApps();
      toast.success(`连接成功，官方目录共 ${total} 个应用`);
      setHhUser('');
      setHhPass('');
      await refreshStatus();
      onCatalogChanged?.();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '无头授权失败');
      await refreshStatus();
    } finally {
      setBusy(false);
    }
  }, [hhUser, hhPass, refreshStatus, onCatalogChanged]);

  const logout = useCallback(async () => {
    setBusy(true);
    try {
      await officialLogout();
      await refreshStatus();
      toast.success('已断开官方应用中心连接');
      onCatalogChanged?.();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '断开失败');
    } finally {
      setBusy(false);
    }
  }, [refreshStatus, onCatalogChanged]);

  const authorized = status?.authorized === true;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {/* 0.6.255：移动端统一卡片设计（与应用详情/向导同款 base card：
          宽 100vw-24px、18px 圆角、居中、四周留白），不再整页铺满——
          手机上更小更好操作；桌面 sm:max-w-lg。X 关闭钮保留右上角。 */}
      <DialogContent className="flex flex-col !p-0 gap-0 overflow-hidden max-h-[calc(100dvh-2rem)] sm:max-w-lg">
        {view === 'status' && (
          <>
            <DialogHeader className="px-5 pt-5 pb-2">
              <div className="flex items-center gap-2">
                <ShieldCheck className="h-5 w-5 text-primary" />
                <DialogTitle className="text-base">官方应用中心</DialogTitle>
                {authorized
                  ? <Badge className="h-5 gap-1 bg-emerald-500/15 px-1.5 text-[10px] text-emerald-600 dark:text-emerald-400"><CheckCircle2 className="h-3 w-3" />已连接</Badge>
                  : <Badge variant="secondary" className="h-5 px-1.5 text-[10px] text-muted-foreground">未连接</Badge>}
              </div>
              <DialogDescription className="text-xs leading-relaxed">
                连接后，官方目录与应用详情通过 OAuth 令牌直取（令牌临期自动续期，无需人工干预）。断开后官方目录暂时不可用，重新连接即可恢复。
              </DialogDescription>
            </DialogHeader>
            <div className="flex-1 space-y-4 overflow-y-auto px-5 py-4">
              {authorized && (
                <div className="rounded-lg border border-emerald-500/30 bg-emerald-500/5 px-3 py-2.5 text-xs text-emerald-700 dark:text-emerald-300">
                  <div className="flex items-center gap-1.5 font-medium"><CheckCircle2 className="h-3.5 w-3.5" />令牌有效</div>
                  {status?.expires_at ? (
                    <div className="mt-0.5 text-muted-foreground">
                      有效期至 {new Date(status.expires_at).toLocaleString()}（临期自动刷新）
                    </div>
                  ) : null}
                </div>
              )}
              {status?.last_error && !authorized && (
                <div className="flex items-start gap-1.5 rounded-lg border border-red-500/30 bg-red-500/5 px-3 py-2.5 text-xs text-red-600 dark:text-red-400">
                  <XCircle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                  <span className="break-all">{status.last_error}</span>
                </div>
              )}
              {/* 0.6.254：面板前端授权 UI 支持门控（旧版 fnOS 面板的 /signin 只渲染
                  普通登录页，PKCE 授权页仅 fnOS 1.2.0800+ 前端有） */}
              {!authorized && status?.ui_known === false && (
                <div className="flex items-center gap-1.5 rounded-lg border border-border/40 bg-muted/30 px-3 py-2.5 text-xs text-muted-foreground">
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />正在检测面板版本支持…
                </div>
              )}
              {!authorized && status?.ui_known && status?.ui_supported === false && (
                <>
                <div className="flex items-start gap-1.5 rounded-lg border border-amber-500/30 bg-amber-500/5 px-3 py-2.5 text-xs leading-relaxed text-amber-700 dark:text-amber-300">
                  <XCircle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                  <span>
                    当前 fnOS 面板版本不支持页面内授权（需 fnOS 1.2.0800 及以上）。
                    用下方「一键免登录授权」：临时填写面板账号一次性登录取码，
                    授权完成后官方目录走 OAuth 令牌，不再触发面板登录。
                  </span>
                </div>
                {/* 0.6.255：临时面板账号（仅本次授权使用，服务端不落盘） */}
                <div className="space-y-2 rounded-lg border border-border/30 bg-muted/20 p-2.5">
                  <div className="flex items-center gap-1.5 text-[11px] font-medium text-muted-foreground">
                    <KeyRound className="h-3 w-3" />
                    本机 Web 面板账号（仅本次使用，不保存）
                  </div>
                  <Input
                    value={hhUser}
                    onChange={(e) => setHhUser(e.target.value)}
                    placeholder="面板登录账号，如 fnos"
                    className="h-9 text-xs"
                    autoComplete="username"
                  />
                  <Input
                    type="password"
                    value={hhPass}
                    onChange={(e) => setHhPass(e.target.value)}
                    placeholder="面板登录密码"
                    className="h-9 text-xs"
                    autoComplete="new-password"
                  />
                </div>
                </>
              )}
              {/* 面板地址输入：仅页面内授权（iframe）流程需要；无头授权走本机回环 */}
              {status?.ui_supported !== false && (
              <div className="space-y-1.5">
                <label className="text-xs text-muted-foreground" htmlFor="official-panel-base">
                  面板地址（授权页打开位置，一般无需修改）
                </label>
                <Input
                  id="official-panel-base"
                  value={base}
                  onChange={(e) => setBase(e.target.value)}
                  placeholder="http://192.168.x.x:5666"
                  className="h-9 font-mono text-xs"
                />
              </div>
              )}
              <div className="flex items-center justify-between gap-2 pt-1">
                <Button variant="ghost" size="sm" className="h-8 text-muted-foreground" onClick={() => onOpenChange(false)}>
                  关闭
                </Button>
                {authorized ? (
                  <Button variant="outline" size="sm" className="h-8 text-red-600 dark:text-red-400" onClick={logout} disabled={busy}>
                    {busy ? <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" /> : <LogOut className="mr-1 h-3.5 w-3.5" />}
                    断开连接
                  </Button>
                ) : status?.ui_supported === false ? (
                  // 0.6.254：旧版 fnOS 前端无授权页 → 走无头授权
                  <Button size="sm" className="h-8" onClick={headlessAuth} disabled={busy}>
                    {busy ? <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" /> : <KeyRound className="mr-1 h-3.5 w-3.5" />}
                    一键免登录授权
                  </Button>
                ) : (
                  <Button size="sm" className="h-8" onClick={startAuthorize} disabled={busy}>
                    {busy ? <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" /> : <KeyRound className="mr-1 h-3.5 w-3.5" />}
                    连接官方应用中心
                  </Button>
                )}
              </div>
            </div>
          </>
        )}

        {view === 'iframe' && (
          <div className="relative flex flex-1 flex-col">
            {/* 顶栏：返回按钮 + 标题（右侧 pr-12 让位卡片右上角 X 关闭钮） */}
            <div className="flex items-center gap-2 border-b border-border px-3 py-2.5 pr-12">
              <button
                type="button"
                onClick={() => setView('code')}
                className="back-wing -ml-1 flex h-9 w-9 shrink-0 select-none items-center justify-center rounded-full border border-black/5 bg-white/75 text-muted-foreground shadow-sm transition-colors hover:bg-white dark:border-white/10 dark:bg-white/10 dark:text-white/70 dark:hover:bg-white/20"
                title="返回页面输入验证码"
                aria-label="返回页面输入验证码"
              >
                <ChevronLeft className="h-5 w-5" strokeWidth={2.5} />
              </button>
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-medium">授权官方应用中心</div>
                <div className="truncate text-[11px] text-muted-foreground">
                  在下方页面登录并复制验证码，然后点左上角 ‹ 返回输入
                </div>
              </div>
              <a
                href={authUrl}
                target="_blank"
                rel="noreferrer"
                className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full text-muted-foreground hover:text-foreground"
                title="若内嵌页面异常，可在新标签页打开"
                aria-label="新标签页打开授权页"
              >
                <ExternalLink className="h-4 w-4" />
              </a>
            </div>
            {/* 0.6.255：内嵌授权页给固定高度（卡片内不再无限拉伸，
                手机上单手可看到「返回输入」顶栏 + 授权页主体） */}
            <div className="bg-muted/30 p-2">
              <iframe
                key={authUrl}
                src={authUrl}
                title="官方应用中心授权"
                className="h-[min(52dvh,460px)] w-full rounded-lg border border-border bg-white"
              />
            </div>
            {/* 底部返回：卡片内用常规按钮（不再用整页悬浮钮） */}
            <div className="border-t border-border px-3 py-2.5">
              <Button variant="outline" size="sm" className="w-full h-9" onClick={() => setView('code')}>
                <ChevronLeft className="mr-1 h-4 w-4" strokeWidth={2.5} />
                返回输入验证码
              </Button>
            </div>
          </div>
        )}

        {view === 'code' && (
          <>
            <DialogHeader className="px-5 pt-5 pb-2">
              <DialogTitle className="text-base">输入验证码</DialogTitle>
              <DialogDescription className="text-xs leading-relaxed">
                复制授权页显示的验证码（一次性，5 分钟内有效），粘贴到下方完成连接。
              </DialogDescription>
            </DialogHeader>
            <div className="flex-1 space-y-4 overflow-y-auto px-5 py-4">
              <Input
                value={code}
                onChange={(e) => setCode(e.target.value)}
                placeholder="例如：A1B2C3D4E5"
                autoFocus
                className="h-12 text-center font-mono text-base tracking-widest"
                onKeyDown={(e) => { if (e.key === 'Enter') void submitCode(); }}
              />
              <div className="flex items-center justify-between gap-2">
                <Button variant="ghost" size="sm" className="h-9" onClick={cancelAuth}>
                  取消授权
                </Button>
                <div className="flex items-center gap-2">
                  <Button variant="outline" size="sm" className="h-9" onClick={() => setView('iframe')}>
                    重新打开授权页
                  </Button>
                  <Button size="sm" className="h-9" onClick={submitCode} disabled={busy || !code.trim()}>
                    {busy ? <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" /> : <KeyRound className="mr-1 h-3.5 w-3.5" />}
                    提交验证码
                  </Button>
                </div>
              </div>
              <button
                type="button"
                onClick={() => setView('status')}
                className="mx-auto flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
              >
                <ChevronLeft className="h-3.5 w-3.5" strokeWidth={2.5} />返回
              </button>
            </div>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
};

export default OfficialOAuthDialog;
