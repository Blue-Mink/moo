import React, { useState, useEffect, useCallback, useRef } from 'react';
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle,
} from "@/components/ui/dialog";
import {
  AlertDialog, AlertDialogCancel, AlertDialogContent, AlertDialogDescription,
  AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import {
  Loader2, ChevronLeft, CheckCircle2, XCircle, KeyRound, LogOut,
  ShieldCheck, ExternalLink,
} from 'lucide-react';
import {
  fetchOfficialStatus, officialAuthorize, officialCallback, officialCancel,
  officialLogout, officialAuthorizeHeadless, fetchOfficialApps, isLanHost,
  setOfficialSessionEncryption, officialErrorCodeOf, updateSettings,
  type OfficialStatus, type OfficialScopeMode,
} from '../api/client';
import { toast } from 'sonner';
import { panelBaseForBrowser } from '../lib/panelBase';

/**
 * 2026-11-26（加固 C）：官方源的写操作是否可用。
 *
 * 后端 GET /api/official/status 现在按身份裁剪，can_admin === false 表示当前账号
 * 没有官方源管理权限（面板地址 / 错误详情也已被裁掉）→ 前端同步收起写入口，
 * 只保留只读状态。can_admin 缺省（旧后端）视为有权限，行为零变化。
 */
export const officialWriteAllowed = (status: OfficialStatus | null | undefined): boolean =>
  status?.can_admin !== false;

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

// ── 面板基址自动识别（0.6.270）─────────────────────────────────────────────
// 历史坑：旧版只按协议猜「同主机 :5666/:5667」，只在局域网 IP 直连时成立；
// FN Connect 中继域、knock 子域（网关端口如 7999/8443）、ESA/EdgeOne 免端口
// 等网关模式下，面板只挂在浏览器当前入口（origin）上，局域网端口从外网不通
// → 授权页 iframe 打不开 / 要手动改端口。
//
// 现在三层判定（命中即停）：
//  ① localStorage 记忆：上次走通的地址直接复用（网关拓扑是稳定配置，
//     一次确认长期有效）；点「自动」钮可清记忆回到 ②③ 探测。
//  ② 非内网主机（公网域名/公网 IP/中继域，isLanHost=false）= 网关/中继模式：
//     base = 浏览器当前 origin —— 端口天然跟随（7999/443/免端口全对，
//     不写死任何网关端口）。
//  ③ 内网主机（私网 IP/.local/.home.arpa/回环）= 局域网直连：
//     面板 = 同主机 5666(http)/5667(https)；0.6.349 起状态接口返回后端
//     解析的 panel_base（读网关配置端口 / 探测 / 5666 兜底），打开对话框
//     后自动替换为真实端口 → 自定义面板端口不再需要手填（issue moo#7）。
// 输入框仍可手动覆盖任意地址（最终兜底）。

const PANEL_BASE_KEY = 'moo.officialPanelBase';
// 0.6.537（用户令）：Moo 由面板网关托管（/app/<name>）时，授权页与 Moo 同源最稳——
// 端口/协议/证书都是浏览器已经信任的那一套，不再按 5666/5667 猜（猜错就会
// 出现「http 打不开、只有 https 能开、但新标签页又能开」的情况）。
const SERVED_BY_PANEL_GATEWAY = typeof window !== 'undefined'
  && /^\/app\/[^/]+/.test(window.location.pathname);
const loadSavedBase = (): string => {
  try { return localStorage.getItem(PANEL_BASE_KEY) || ''; } catch { return ''; }
};
const saveBase = (b: string) => {
  try { localStorage.setItem(PANEL_BASE_KEY, b.trim()); } catch { /* 忽略 */ }
};
const clearSavedBase = () => {
  try { localStorage.removeItem(PANEL_BASE_KEY); } catch { /* 忽略 */ }
};

const guessPanelBase = (): string => {
  const saved = loadSavedBase();
  if (saved) return saved;
  if (SERVED_BY_PANEL_GATEWAY) return window.location.origin;
  const host = window.location.hostname || 'localhost';
  if (!isLanHost(host.toLowerCase())) {
    // 网关/中继模式：面板与浏览器同入口，origin 自带正确协议与端口
    return window.location.origin;
  }
  // 局域网直连：Moo 与面板同主机，面板走 5666(http)/5667(https)
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
  // 0.6.270：内嵌授权页 8s 未加载完成 → 提示（网关可能拦 iframe 或入口地址不对）
  const [slowTip, setSlowTip] = useState(false);
  // 0.6.255：无头授权的临时面板账号（旧版 fnOS 前端无授权 UI 时用；不保存）
  const [hhUser, setHhUser] = useState('');
  const [hhPass, setHhPass] = useState('');
  // 0.6.349：用户手动改过面板地址 → 不被后端解析值覆盖
  const baseDirtyRef = useRef(false);
  // 0.6.537（用户令）：新标签页授权——内嵌窗口被自签名证书/跨源 Cookie 拦住时，
  // 新标签页是唯一可靠路径（浏览器允许在顶层页面确认证书）。
  const [openedInTab, setOpenedInTab] = useState(false);
  const [popupBlocked, setPopupBlocked] = useState(false);
  // 2026-11-26（加固 B2）：会话落盘加密开关（opt-in，默认关）。
  // encConfirm = 开启前的确认面板（列出三条代价，避免用户不知道后果就打开）。
  const [encBusy, setEncBusy] = useState(false);
  const [encConfirm, setEncConfirm] = useState(false);
  // 2026-11-26（加固 A）：授权范围（full=完整权限默认，minimal=仅应用中心）。
  // 初值来自后端配置（status.scope_mode）；用户切换时立即写回设置（**不用
  // localStorage**：设置随配置文件走，换浏览器/清缓存后仍是上次的选择）。
  // scopeTouchedRef = 本次会话内用户已明确选过 → 不随后端刷新覆盖。
  const [scopeMode, setScopeMode] = useState<OfficialScopeMode>('full');
  const scopeTouchedRef = useRef(false);
  // 只有后端明确说「支持最小权限」（scope_minimal_supported=true）才展示选项、
  // 才在请求里带 scope：旧后端（无该字段）→ 不发 scope 参数、不出现新 UI，
  // **请求与界面都与改动前逐字节一致**（零行为变化）。
  const minimalSupported = status?.scope_minimal_supported === true;
  // 2026-11-27（批次 3c · D4）：面板拒绝 minimal 时**不再静默扩权**。
  // scopeConfirm = 显式确认框是否打开；scopeCancelHint = 用户点了「取消」后
  // 在验证码视图留下的可操作提示（说明未做任何改动 + 指回手动路径）。
  const [scopeConfirm, setScopeConfirm] = useState(false);
  const [scopeCancelHint, setScopeCancelHint] = useState(false);

  // 0.6.537：授权页风险判定（不改安全策略，只决定默认推荐哪种打开方式）
  const baseInfo = React.useMemo(() => {
    try {
      const u = new URL(base.trim());
      return { origin: u.origin, https: u.protocol === 'https:' };
    } catch { return null; }
  }, [base]);

  // ── 0.6.537：新标签页授权（跨源 / 自签名证书 / 内嵌被拦时的可靠路径） ──
  // 说明：回调定义在 startAuthorize 之后（需要先有 setView 等状态）。
  const insecurePage = typeof window !== 'undefined' && !window.isSecureContext;
  const crossOrigin = !!baseInfo && baseInfo.origin !== window.location.origin;
  const preferNewTab = crossOrigin;

  const refreshStatus = useCallback(async () => {
    try {
      const s = await fetchOfficialStatus();
      setStatus(s);
      // 2026-11-26（加固 A）：水合授权范围偏好（缺省/旧后端 → full）。
      if (!scopeTouchedRef.current) {
        setScopeMode(s.scope_mode === 'minimal' ? 'minimal' : 'full');
      }
      // 0.6.349（issue moo#7）：后端解析出的面板基址带用户自定义端口，
      // 局域网直连时优先于前端猜测（回环 → 页面主机名，端口保留）；
      // 网关/中继模式仍用当前 origin（端口天然跟随）。用户手改/已记忆不覆盖。
      if (s.panel_base && !baseDirtyRef.current && !loadSavedBase()
          && !SERVED_BY_PANEL_GATEWAY
          && isLanHost(window.location.hostname.toLowerCase())) {
        setBase(panelBaseForBrowser(s.panel_base, window.location.hostname));
      }
    } catch {
      setStatus(null);
    }
  }, []);

  useEffect(() => {
    if (open) {
      setView('status');
      setCode('');
      setBusy(false);
      setOpenedInTab(false);
      setPopupBlocked(false);
      // 批次 3c · D4：确认框与取消提示都是本次授权内的瞬时状态，重开即清。
      setScopeConfirm(false);
      setScopeCancelHint(false);
      // 每次打开都重新跟随后端配置（上次选择已写进 config，见 chooseScope）。
      scopeTouchedRef.current = false;
      void refreshStatus();
    }
  }, [open, refreshStatus]);

  // 0.6.270：内嵌授权页 8s 未加载 → 出提示条（不自动关，给用户选择）
  useEffect(() => {
    if (view !== 'iframe') {
      setSlowTip(false);
      return;
    }
    const t = setTimeout(() => setSlowTip(true), 8000);
    return () => clearTimeout(t);
  }, [view, authUrl]);

  // 0.6.270：清掉记忆地址，回到自动探测（域名→origin / IP→5666/5667）
  const resetBaseAuto = useCallback(() => {
    clearSavedBase();
    baseDirtyRef.current = false;
    // 局域网直连且后端已解析出端口 → 回到解析值；否则回自动识别
    if (status?.panel_base && !SERVED_BY_PANEL_GATEWAY && isLanHost(window.location.hostname.toLowerCase())) {
      setBase(panelBaseForBrowser(status.panel_base, window.location.hostname));
    } else {
      setBase(guessPanelBase());
    }
  }, [status]);

  // 2026-11-26（加固 A）：记住授权范围 —— 写设置（config），失败只提示不改 UI。
  const chooseScope = useCallback(async (m: OfficialScopeMode) => {
    scopeTouchedRef.current = true;
    setScopeMode(m);
    try {
      await updateSettings({ official_scope_mode: m });
    } catch {
      toast.error('授权范围偏好未能保存（本次仍然按所选范围发起）');
    }
  }, []);

  // ── 发起授权（生成链接 → 切 iframe 视图） ──
  // scopeOverride：自动落回时显式传 'full'，忽略当前 UI 选择。
  const startAuthorize = useCallback(async (scopeOverride?: OfficialScopeMode) => {
    setBusy(true);
    try {
      const { url } = await officialAuthorize(
        base.trim() || undefined,
        scopeOverride ?? (minimalSupported ? scopeMode : undefined),
      );
      setAuthUrl(url);
      setView('iframe');
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '发起授权失败');
    } finally {
      setBusy(false);
    }
  }, [base, scopeMode, minimalSupported]);

  // ── 0.6.537：新标签页授权（跨源 / 自签名证书 / 内嵌被拦时的可靠路径） ──
  // 同步先开空白页（必须在用户手势内），拿到 URL 后再导航——否则 await 之后
  // 再 window.open 会被浏览器按弹窗拦截。
  const openAuthTab = useCallback(async (scopeOverride?: OfficialScopeMode) => {
    let win: Window | null = null;
    try { win = window.open('', '_blank'); } catch { win = null; }
    if (win) { try { win.opener = null; } catch { /* 忽略 */ } }
    setPopupBlocked(false);
    setBusy(true);
    try {
      const { url } = await officialAuthorize(
        base.trim() || undefined,
        scopeOverride ?? (minimalSupported ? scopeMode : undefined),
      );
      setAuthUrl(url);
      setOpenedInTab(true);
      setView('code');
      if (win) {
        win.location.href = url;
        toast.info('已在新标签页打开授权页：登录并授权后，复制验证码粘贴到这里');
      } else {
        setPopupBlocked(true);
        toast.warning('浏览器拦截了新标签页，请点下方「打开授权页」继续');
      }
    } catch (e) {
      if (win) { try { win.close(); } catch { /* 忽略 */ } }
      toast.error(e instanceof Error ? e.message : '发起授权失败');
    } finally {
      setBusy(false);
    }
  }, [base, scopeMode, minimalSupported]);

  // ── 提交验证码 → 换 token → 拉目录验证 ──
  const submitCode = useCallback(async () => {
    const c = code.trim();
    if (!c) return;
    setBusy(true);
    try {
      await officialCallback(c);
      // 0.6.270：整条流程（含 iframe 授权页）走通 → 记住该面板地址
      saveBase(base);
      // 验证：拉官方全量目录（证明 token 可用）
      const { total } = await fetchOfficialApps();
      toast.success(`连接成功，官方目录共 ${total} 个应用`);
      setCode('');
      await refreshStatus();
      setView('status');
      onCatalogChanged?.();
    } catch (e) {
      // 2026-11-27（批次 3c · D4）：面板拒绝 minimal → **不再静默扩权**。
      // 旧实现直接 chooseScope('full') + 以 full 重发，等于替用户把「仅应用中心」
      // 悄悄换成「完整权限」（可读面板应用管理，权限更大）。现在改为暂停流程、
      // 弹确认框：只有用户点「改用完整权限重试」才写 config 并以 full 重发；
      // 点「取消」则什么都不改（不写 config、不发请求），只留一条可操作提示。
      // ⚠️ 取消分支绝不能调 chooseScope —— 它会写 official_scope_mode。
      if (officialErrorCodeOf(e) === 'scope_rejected') {
        setCode('');
        setScopeCancelHint(false);
        setScopeConfirm(true);
        // 只发一条提示（避免「toast + 弹窗」双份噪音）。
        toast.info('面板不支持「仅应用中心」授权范围，请确认是否改用完整权限重试');
        return;
      }
      toast.error(e instanceof Error ? e.message : '验证码无效或已过期（5 分钟内有效，请重新授权）');
    } finally {
      setBusy(false);
    }
  }, [code, refreshStatus, onCatalogChanged]);

  // 手动「改用完整权限重试」：覆盖「授权页根本没打开」——那种情况前端拿不到
  // error_code（没有回调），只能靠用户点一下换范围重发。
  const retryWithFull = useCallback(async () => {
    toast.info('已改用完整权限重新发起授权');
    setScopeCancelHint(false);
    await chooseScope('full');
    await startAuthorize('full');
  }, [chooseScope, startAuthorize]);

  // 2026-11-27（批次 3c · D4）：scope_rejected 确认框的两条分支。
  // 确认 = 唯一会写 config 的路径（chooseScope 写 official_scope_mode=full）→ 以 full 重发。
  const confirmFullScope = useCallback(async () => {
    setScopeConfirm(false);
    setScopeCancelHint(false);
    await chooseScope('full');
    await startAuthorize('full');
  }, [chooseScope, startAuthorize]);

  // 取消 = 明确不扩权：不写 config、不发请求，停留在当前（验证码）视图，
  // 只留一条可操作提示（指回既有手动「改用完整权限重试」，并说明取消=本次不连接）。
  const cancelFullScope = useCallback(() => {
    setScopeConfirm(false);
    setScopeCancelHint(true);
  }, []);

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
      await officialAuthorizeHeadless(hhUser.trim(), hhPass, minimalSupported ? scopeMode : undefined);
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
  }, [hhUser, hhPass, scopeMode, minimalSupported, refreshStatus, onCatalogChanged]);

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

  // 2026-11-26（加固 B2）：切换会话加密（开启前必须先过确认面板）。
  const applySessionEncryption = useCallback(async (enabled: boolean) => {
    setEncBusy(true);
    try {
      await setOfficialSessionEncryption(enabled);
      toast.success(enabled ? '已改为加密保存会话凭据' : '已改回明文保存会话凭据');
      await refreshStatus();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '切换会话加密失败');
    } finally {
      setEncBusy(false);
      setEncConfirm(false);
    }
  }, [refreshStatus]);

  const authorized = status?.authorized === true;
  // 2026-11-26（加固 C）：非管理员 = 只读（隐藏发起授权 / 临时登录 / 断开连接）。
  const canManageOfficial = officialWriteAllowed(status);
  // 会话加密：缺省（旧后端无该字段）= 视为可用，避免把开关误禁用；
  // 只有后端明确 encryption_available === false 才禁用。
  const sessionEncrypted = status?.session_encrypted === true;
  // 2026-11-26（盲点修复）：开关显示「用户意图」，而不是只看磁盘形态。
  // 未连接时开启加密只改意图（还没会话可写），旧实现让开关弹回「关」。
  // 旧后端没有该字段 → 回退到磁盘形态，行为不变。
  const encryptionOn = typeof status?.session_encryption_on === 'boolean'
    ? status.session_encryption_on
    : sessionEncrypted;
  const encryptionMismatch = status?.encryption_mismatch === true;
  const encAvailable = status?.encryption_available !== false;
  // 2026-11-26（加固 A）：已连接时把「当前令牌实际拿到多宽的权限」说清楚——
  // 只读 scopes 列表（面板回给 token 的真实范围），不猜配置，避免把「偏好」
  // 误当成「已生效」。
  const tokenScopeLabel = (() => {
    const sc = status?.scopes || [];
    if (sc.length === 0) return null;
    return sc.length === 1 && sc[0] === 'trim.appcenter.all'
      ? '仅应用中心（1 项）'
      : `完整权限（${sc.length} 项）`;
  })();

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
                  {tokenScopeLabel ? (
                    <div data-testid="official-token-scope" className="mt-0.5 text-muted-foreground">
                      当前令牌权限：{tokenScopeLabel}
                    </div>
                  ) : null}
                </div>
              )}
              {/* 2026-11-26（加固 C）：后端按身份裁剪（can_admin=false）→ 明确告知
                  为什么没有连接按钮，而不是让用户点了之后吃 403。 */}
              {!canManageOfficial && (
                <div
                  data-testid="official-admin-only"
                  className="flex items-start gap-1.5 rounded-lg border border-border/40 bg-muted/30 px-3 py-2.5 text-xs leading-relaxed text-muted-foreground"
                >
                  <ShieldCheck className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                  <span>
                    仅管理员可连接或授权官方源：当前账号没有管理员权限，只能查看连接状态；
                    发起授权、临时登录面板与断开连接都需要管理员权限。
                    请在飞牛面板「设置 → 用户」中授予该账号管理员权限后刷新本页。
                  </span>
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
                    可用下方「临时登录面板」：临时填写面板账号一次性登录，
                    经面板会话获取官方目录；账号仅本次使用，不保存。
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
              {/* 2026-11-26（加固 A）：授权范围自选（默认 full = 零行为变化）。
                  只有管理员能发起授权，故与写入口同样只在 canManageOfficial 时出现；
                  已连接时不展示（改范围要先断开再重授权，避免误解为立即收紧）。 */}
              {canManageOfficial && !authorized && minimalSupported && (
                <div data-testid="official-scope-choices" className="space-y-1.5">
                  <div className="text-xs text-muted-foreground">授权范围（决定 Moo 能代表你对面板做什么）</div>
                  <div role="radiogroup" aria-label="授权范围" className="grid gap-1.5 sm:grid-cols-2">
                    <button
                      type="button"
                      role="radio"
                      aria-checked={scopeMode === 'full'}
                      data-testid="official-scope-full"
                      onClick={() => void chooseScope('full')}
                      className={`rounded-lg border px-3 py-2 text-left transition-colors ${scopeMode === 'full'
                        ? 'border-primary bg-primary/5'
                        : 'border-border/40 bg-muted/20 hover:bg-muted/40'}`}
                    >
                      <div className="flex items-center gap-1.5 text-xs font-medium">
                        {scopeMode === 'full'
                          ? <CheckCircle2 className="h-3.5 w-3.5 text-primary" />
                          : <span className="h-3.5 w-3.5 rounded-full border border-muted-foreground/40" />}
                        完整权限（默认，兼容旧版）
                      </div>
                      <p className="mt-0.5 text-[11px] leading-relaxed text-muted-foreground">
                        与历史版本一致：文件读写、下载、系统设置、日志、存储、Docker 等全部权限。
                        所有面板版本都支持。
                      </p>
                    </button>
                    <button
                      type="button"
                      role="radio"
                      aria-checked={scopeMode === 'minimal'}
                      data-testid="official-scope-minimal"
                      onClick={() => void chooseScope('minimal')}
                      className={`rounded-lg border px-3 py-2 text-left transition-colors ${scopeMode === 'minimal'
                        ? 'border-primary bg-primary/5'
                        : 'border-border/40 bg-muted/20 hover:bg-muted/40'}`}
                    >
                      <div className="flex items-center gap-1.5 text-xs font-medium">
                        {scopeMode === 'minimal'
                          ? <CheckCircle2 className="h-3.5 w-3.5 text-primary" />
                          : <span className="h-3.5 w-3.5 rounded-full border border-muted-foreground/40" />}
                        仅应用中心（推荐）
                      </div>
                      <p className="mt-0.5 text-[11px] leading-relaxed text-muted-foreground">
                        只能读取官方应用目录与版本信息，不能读写文件、不能改系统设置。
                        老面板可能不支持，届时会先询问你是否改用完整权限重试。
                      </p>
                    </button>
                  </div>
                </div>
              )}
              {/* 面板地址输入：仅页面内授权（iframe）流程需要；无头授权走本机回环 */}
              {canManageOfficial && status?.ui_supported !== false && (
              <div className="space-y-1.5">
                <label className="text-xs text-muted-foreground" htmlFor="official-panel-base">
                  面板地址（授权页打开位置，自动识别：网关入口=当前地址，局域网 IP=本机面板端口）
                </label>
                <div className="flex gap-1.5">
                  <Input
                    id="official-panel-base"
                    value={base}
                    onChange={(e) => { baseDirtyRef.current = true; setBase(e.target.value); }}
                    placeholder="http://192.168.x.x:5666"
                    className="h-9 flex-1 font-mono text-xs"
                  />
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-9 shrink-0 px-2.5 text-[11px] text-muted-foreground"
                    onClick={resetBaseAuto}
                    title="清除已记住的地址，回到自动识别"
                  >
                    自动
                  </Button>
                </div>
              </div>
              )}
              {/* 0.6.537（用户令）：把「http 为什么打不开 / 为什么要新标签页」讲清楚；
                  只影响提示与默认推荐，不改变安全策略（授权页始终是面板自己的 /signin，
                  仍走 PKCE + 一次性验证码）。 */}
              {canManageOfficial && !authorized && (insecurePage || crossOrigin) && (
                <div className="rounded-lg border border-border/40 bg-muted/30 px-3 py-2.5 text-[11px] leading-relaxed text-muted-foreground">
                  {insecurePage && <>当前通过 HTTP 打开 Moo，部分浏览器会限制内嵌页面的登录（加密接口 / 第三方 Cookie）。</>}
                  {crossOrigin && (
                    <> 授权页 <span className="break-all font-mono">{baseInfo?.origin}</span> 与当前页面不同源；
                      {baseInfo?.https
                        ? '面板若使用 HTTPS 自签名证书，内嵌窗口无法确认证书（顶层标签页可以）。'
                        : '内嵌页面可能因 Cookie 限制无法保持登录。'}</>
                  )}
                  {' '}内嵌打不开时，请用「新标签页授权」。
                </div>
              )}
              {/* 2026-11-26（加固 B2）：会话凭据加密保存（高级，opt-in，默认关）。
                  只对管理员可见：非管理员改了也不会生效（requireAdmin 403）。 */}
              {canManageOfficial && (
                <div
                  data-testid="official-session-encryption"
                  className="space-y-2 rounded-lg border border-border/30 bg-muted/20 p-2.5"
                >
                  <div className="text-[11px] font-medium text-muted-foreground">高级</div>
                  <div className="flex items-start justify-between gap-2">
                    <div className="space-y-0.5">
                      <label
                        htmlFor="official-session-encrypt"
                        className="text-[11px] font-medium text-muted-foreground"
                      >
                        加密保存会话凭据
                      </label>
                      <p className="text-[11px] leading-relaxed text-muted-foreground">
                        默认关闭。开启后会话文件用本机密钥加密落盘；旧版本读到会显示「未连接」。
                      </p>
                    </div>
                    <Switch
                      id="official-session-encrypt"
                      data-testid="official-session-encrypt-switch"
                      checked={encryptionOn}
                      disabled={encBusy || !encAvailable}
                      onCheckedChange={(v) => {
                        // 开启必须走确认面板（列出代价）；关闭立即生效。
                        if (v) { setEncConfirm(true); return; }
                        void applySessionEncryption(false);
                      }}
                    />
                  </div>
                  {!encAvailable && (
                    <p data-testid="official-encrypt-unavailable" className="text-[11px] text-amber-700 dark:text-amber-300">
                      本机密钥不可用（密钥文件缺失或损坏），无法开启会话加密。
                    </p>
                  )}
                  {encAvailable && sessionEncrypted && (
                    <p data-testid="official-encrypt-on" className="text-[11px] text-emerald-700 dark:text-emerald-300">
                      当前：会话凭据已加密保存。
                    </p>
                  )}
                  {encAvailable && encryptionOn && !sessionEncrypted && (
                    <p data-testid="official-encrypt-pending" className="text-[11px] text-amber-700 dark:text-amber-300">
                      {encryptionMismatch
                        ? '开关为开，但当前会话文件仍是明文（可能被旧版本重新授权覆盖）；下次保存会自动加密。'
                        : '已开启；下次保存会话时按加密形态落盘。'}
                    </p>
                  )}
                  {encAvailable && !encryptionOn && sessionEncrypted && (
                    <p data-testid="official-encrypt-legacy" className="text-[11px] text-amber-700 dark:text-amber-300">
                      当前文件为密文，但开关为关（历史遗留）；下次保存会转回明文。
                    </p>
                  )}
                  {encConfirm && (
                    <div
                      role="alertdialog"
                      aria-label="确认开启会话加密"
                      data-testid="official-encrypt-confirm"
                      className="space-y-2 rounded-md border border-amber-500/30 bg-amber-500/5 p-2.5 text-[11px] leading-relaxed text-amber-800 dark:text-amber-200"
                    >
                      <div className="font-medium">开启前请确认三点代价：</div>
                      <ul className="list-disc space-y-1 pl-4">
                        <li>降级到旧版本、或换机恢复本目录时无法解密，需要重新授权一次。</li>
                        <li>整个数据目录一起备份仍可解密（密钥与密文同目录），除非备份时排除会话文件。</li>
                        <li>随时可以关闭：关闭后立即写回明文。</li>
                      </ul>
                      <div className="flex justify-end gap-1.5">
                        <Button
                          variant="ghost"
                          size="sm"
                          className="h-7 px-2 text-[11px]"
                          onClick={() => setEncConfirm(false)}
                          disabled={encBusy}
                        >
                          取消
                        </Button>
                        <Button
                          size="sm"
                          className="h-7 px-2 text-[11px]"
                          onClick={() => void applySessionEncryption(true)}
                          disabled={encBusy}
                        >
                          {encBusy ? <Loader2 className="mr-1 h-3 w-3 animate-spin" /> : null}
                          确认开启
                        </Button>
                      </div>
                    </div>
                  )}
                </div>
              )}
              <div className="flex items-center justify-between gap-2 pt-1">
                <Button variant="ghost" size="sm" className="h-8 text-muted-foreground" onClick={() => onOpenChange(false)}>
                  关闭
                </Button>
                {!canManageOfficial ? (
                  // 2026-11-26（加固 C）：只读身份不渲染任何写操作按钮（发起授权 /
                  // 临时登录面板 / 断开连接都需管理员；后端同样会 403）。
                  <span data-testid="official-readonly-hint" className="text-[11px] text-muted-foreground">
                    仅管理员可操作
                  </span>
                ) : authorized ? (
                  <Button variant="outline" size="sm" className="h-8 text-red-600 dark:text-red-400" onClick={logout} disabled={busy}>
                    {busy ? <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" /> : <LogOut className="mr-1 h-3.5 w-3.5" />}
                    断开连接
                  </Button>
                ) : status?.ui_supported === false ? (
                  // 0.6.254：旧版 fnOS 前端无授权页 → 走无头授权
                  // 0.6.260：按钮名「临时登录面板」——登录对象是 Web 面板（非整个 NAS），
                  // 与新版面板的「OAuth授权登录」两种登录方式分开、互不混用
                  <Button size="sm" className="h-8" onClick={headlessAuth} disabled={busy}>
                    {busy ? <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" /> : <KeyRound className="mr-1 h-3.5 w-3.5" />}
                    临时登录面板
                  </Button>
                ) : (
                  // 0.6.260：按钮名「OAuth授权登录」——页面内 OAuth 授权码流程
                  // （授权页内登录 + 点授权 + 验证码），与旧面板「临时登录面板」分开。
                  // 0.6.537（用户令）：新增「新标签页授权」——跨源/自签名证书时浏览器
                  // 只允许顶层页面确认证书，内嵌窗口会直接失败；两条路径共用后端生成的
                  // 同一个 PKCE 授权 URL，验证码仍由后端 verifier 校验（5 分钟一次性）。
                  <div className="flex flex-wrap items-center justify-end gap-2">
                    {/* 0.6.538（用户令）：「新标签页授权」移到「OAuth授权登录」前面——
                        内嵌窗口在证书 / Cookie 限制下打不开时，靠前的按钮才是可靠路径。 */}
                    <Button
                      variant={preferNewTab ? 'default' : 'outline'}
                      size="sm"
                      className="h-8"
                      onClick={() => void openAuthTab()}
                      disabled={busy}
                      title="在新标签页打开授权页：内嵌窗口打不开（证书 / Cookie 限制）时用这个"
                    >
                      <ExternalLink className="mr-1 h-3.5 w-3.5" />
                      新标签页授权
                    </Button>
                    <Button variant={preferNewTab ? 'outline' : 'default'} size="sm" className="h-8" onClick={() => void startAuthorize()} disabled={busy}>
                      {busy ? <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" /> : <KeyRound className="mr-1 h-3.5 w-3.5" />}
                      OAuth授权登录
                    </Button>
                  </div>
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
                rel="noopener noreferrer"
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
              {slowTip && (
                <div className="mx-0.5 mb-2 space-y-1.5 rounded-md border border-amber-500/30 bg-amber-500/10 px-2.5 py-1.5 text-[11px] leading-relaxed text-amber-700 dark:text-amber-300">
                  <div className="flex items-start gap-1.5">
                    <Loader2 className="mt-0.5 h-3 w-3 shrink-0 animate-spin" />
                    <span>
                      授权页加载较慢或无法打开（面板 HTTPS 自签名证书、跨源 Cookie 限制、
                      网关拦截内嵌页面都会这样）。
                    </span>
                  </div>
                  {/* 0.6.537（用户令）：给出可点的出口，而不只是让用户找右上角小箭头 */}
                  <div className="flex flex-wrap items-center gap-1.5 pl-[18px]">
                    <Button
                      size="sm"
                      variant="outline"
                      className="h-7 border-amber-600/40 bg-amber-500/10 px-2 text-[11px] text-amber-800 hover:bg-amber-500/20 dark:text-amber-200"
                      onClick={() => void openAuthTab()}
                      disabled={busy}
                    >
                      <ExternalLink className="mr-1 h-3 w-3" />
                      在新标签页打开授权页
                    </Button>
                    <span className="text-[10px] opacity-80">或返回修改「面板地址」后重试</span>
                  </div>
                </div>
              )}
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
              {/* 0.6.537：新标签页授权后的引导（弹窗被拦时给可点的兜底链接） */}
              {(openedInTab || popupBlocked) && (
                <div className="space-y-1.5 rounded-lg border border-border/40 bg-muted/30 px-3 py-2.5 text-[11px] leading-relaxed text-muted-foreground">
                  <div>
                    {openedInTab
                      ? '已在新标签页打开授权页：登录 → 授权 → 复制验证码，回到这里粘贴提交。'
                      : '浏览器拦截了新标签页，请点下方按钮手动打开授权页。'}
                  </div>
                  {authUrl && (
                    <a
                      href={authUrl}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="inline-flex items-center gap-1 text-primary hover:underline"
                    >
                      <ExternalLink className="h-3 w-3" />打开授权页
                    </a>
                  )}
                </div>
              )}
              {/* 2026-11-27（批次 3c · D4）：用户在上一步确认框里选了「取消」——
                  明确说明「什么都没改」，并指回可手动操作的两条路。 */}
              {scopeCancelHint && (
                <div
                  role="status"
                  data-testid="official-scope-cancel-hint"
                  className="flex items-start gap-1.5 rounded-lg border border-border/40 bg-muted/30 px-3 py-2.5 text-[11px] leading-relaxed text-muted-foreground"
                >
                  <XCircle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                  <div className="space-y-1">
                    <div>已取消：授权范围仍是「仅应用中心」，没有改动任何设置，也没有发出任何请求。</div>
                    <div>
                      这个面板不支持最小权限，取消即表示本次不连接（官方目录暂不可用）。
                      要连接可点下方「改用完整权限重试」；连接后随时能在上方「断开连接」收回授权。
                    </div>
                  </div>
                </div>
              )}
              {/* 2026-11-26（加固 A）：手动落回入口。自动落回依赖「回调报
                  error_code=scope_rejected」，而「授权页根本没打开」时前端拿不到
                  任何回调信号 —— 这一条是给那种情况的兜底。 */}
              {scopeMode === 'minimal' && (
                <div
                  data-testid="official-scope-fallback"
                  className="flex items-start gap-1.5 rounded-lg border border-amber-500/30 bg-amber-500/5 px-3 py-2.5 text-[11px] leading-relaxed text-amber-700 dark:text-amber-300"
                >
                  <XCircle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                  <div className="space-y-1">
                    <div>授权页打不开、或验证码一直无效？可能是这个面板不支持最小权限。</div>
                    <button
                      type="button"
                      data-testid="official-scope-retry-full"
                      className="font-medium underline underline-offset-2"
                      onClick={() => void retryWithFull()}
                    >
                      改用完整权限重试
                    </button>
                  </div>
                </div>
              )}
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

      {/* 2026-11-27（批次 3c · D4）：面板拒绝 minimal 时的**显式确认**（不再静默扩权）。
          用项目现有 AlertDialog（role=alertdialog，标题由 AlertDialogTitle 提供）；
          宽度沿用 AlertDialogContent 的 calc(100vw-1.5rem)（窄屏不溢出），
          桌面再收窄到 sm:max-w-md。 */}
      <AlertDialog open={scopeConfirm} onOpenChange={(o) => { if (!o) cancelFullScope(); }}>
        <AlertDialogContent
          data-testid="official-scope-rejected"
          className="rounded-[18px] border-border/20 bg-card shadow-appstore sm:max-w-md"
        >
          <AlertDialogHeader>
            <AlertDialogTitle className="text-base">面板不支持「仅应用中心」授权范围</AlertDialogTitle>
            <AlertDialogDescription className="space-y-1.5 text-xs leading-relaxed">
              <span className="block">
                这个面板拒绝了「仅应用中心」授权范围。目前没有做任何改动
                （授权范围偏好保持原样，也没有发出新的授权请求）。
              </span>
              <span className="block">
                改用<b>完整权限</b>重试吗？完整权限可读面板上的应用管理，并包含文件读写、
                系统设置、日志与 Docker 等能力，<b>权限更大</b>。
              </span>
              <span className="block">
                不想扩大权限就点「取消」：不写设置、不发请求，本次保持未连接
                （官方目录暂不可用）；之后随时可以改用完整权限重连。
              </span>
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            {/* 2026-11-27（批次 3c 复核 MF-1）：取消**只**由 AlertDialog 的
                onOpenChange(false) 收口（本按钮是 Radix Close，关闭即触发一次）。
                这里刻意不挂 onClick —— 否则同一次点击会执行两遍 cancelFullScope。 */}
            <AlertDialogCancel
              data-testid="official-scope-rejected-cancel"
              className="h-9 text-xs"
            >
              取消
            </AlertDialogCancel>
            <Button
              data-testid="official-scope-rejected-confirm"
              className="h-9 text-xs"
              onClick={() => void confirmFullScope()}
            >
              改用完整权限重试
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Dialog>
  );
};

export default OfficialOAuthDialog;
