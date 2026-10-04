import { apiUrl } from './base';
import { formatCount } from '../lib/utils';

// 0.6.207-panel 安全（D2 CSRF 纵深防御）：所有非 GET 的 /api 请求附带自定义头
// X-Moo-Admin。跨站 simple 请求（表单 POST / no-cors fetch）无法附加自定义头
//（需 CORS 预检，而网关无 CORS 头 → 浏览器直接拦截），即使平台网关不校验
// Origin，CSRF 也在应用层被封死。后端 requireAdmin 强制校验该头。
const nativeFetch: typeof fetch = window.fetch.bind(window);
export const apiFetch = (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
  const method = (init?.method || 'GET').toUpperCase();
  if (method !== 'GET' && method !== 'HEAD' && method !== 'OPTIONS') {
    const headers = new Headers(init?.headers);
    headers.set('X-Moo-Admin', '1');
    init = { ...init, headers };
  }
  return nativeFetch(input, init);
};

export interface AppInfo {
  /** 注册表内部键（外部源应用为 appname@源名）；同名应用共存时用它做唯一标识。 */
  key: string;
  appname: string;
  display_name: string;
  description?: string;
  installed: boolean;
  installed_version: string;
  latest_version: string;
  /**
   * Package versions. `installed_version` / `latest_version` are UPSTREAM
   * strings produced by two different sources and can disagree for the same
   * package (headscale reports installed 0.29.7 against catalog 0.29.3 while
   * both sides are 0.29.3-rN packages). The fpk pair below is what the backend
   * actually compares, so prefer it for display.
   */
  installed_fpk_version?: string;
  available_version?: string;
  has_update: boolean;
  update_ignored?: boolean;
  /** 已忽略且确有被压住的更新（0.6.197：dock「有更新」列表含它） */
  update_ignored_pending?: boolean;
  platform: string;
  release_url: string;
  release_notes: string;
  /**
   * 应用中心 daemon 上报的运行状态：running / stopped / starting / stopping /
   * nostart（系统组件，无独立启停）等。未安装应用为空。
   */
  status: string;
  /** daemon 能力位：是否支持启动/停用（nostart 系统组件为 false）。缺省按支持处理。 */
  start_stop?: boolean;
  /** daemon 能力位：是否可卸载。缺省按可卸载处理。 */
  uninstallable?: boolean;
  /**
   * 已安装应用的可打开 Web 入口（daemon appServiceInfo，与应用中心"打开"同源）。
   * web_url 在 daemon 提供了 host 时为完整 URL；否则用 web_protocol/web_port/
   * web_path 由前端按当前访问主机拼出（直达 :38011 或 Web UI 内嵌 iframe 均成立）。
   */
  web_protocol?: string;
  web_url?: string;
  web_port?: number;
  web_path?: string;
  /** Web 入口由 fnOS Web UI 自身服务（:5666 + web_path），无独立端口。 */
  web_on_webui?: boolean;
  /** daemon appServiceInfo.serviceName（如 "Gitea.Application"）：
   *  内嵌 fnOS Web UI 时"打开"走壳窗口 openApp(serviceName) 在壳内打开。 */
  web_service_name?: string;
  service_port?: number;
  homepage?: string;
  icon_url?: string;
  updated_at?: string;
  download_count?: number;
  /** 本机安装/更新次数（第三方源应用无全局下载量时回退展示「本机 N 次」）。 */
  local_installs?: number;
  app_type?: string;
  category?: string;
  post_install_note?: string;
  /** 应用来自哪个目录源（内置目录无此字段；外部 FnDepot 源为其显示名）。 */
  source?: string;
  // 外部源详情页扩展元数据（内置目录应用无这些字段）。
  maintainer?: string;
  maintainer_url?: string;
  distributor?: string;
  distributor_url?: string;
  changelog?: string;
  /** changelog 解析后的版本化条目（最新在前）；无 changelog 时缺省。 */
  changelog_entries?: { version?: string; text: string }[];
  size_bytes?: number;
  /** moo.json 扩展：应用运行方式/安装位置（root / 用户空间 / 系统空间） */
  install_type?: string;
  /** moo.json 扩展：该应用最早发布时间 */
  first_release_at?: string;
  /** moo.json 扩展：富文本简介（后端已原样下发；前端消毒后渲染） */
  desc_html?: string;
  /** moo.json 扩展：许可协议（如 MIT） */
  license?: string;
  /** moo.json 扩展：最低 fnOS 版本（详情页提示；安装期由平台校验） */
  min_fnos?: string;
  /** moo.json 扩展：源声明的安装向导参数（安装时收集，键名由应用定义） */
  wizard?: {
    fields?: {
      key: string;
      label?: string;
      default?: string;
      required?: boolean;
    }[];
  };
  sha256?: string;
  preview_count?: number;
  has_readme?: boolean;
}

/**
 * The installed version to SHOW. Prefers the package version the backend
 * compares on, so it lines up with `available_version` instead of pairing two
 * unrelated upstream strings. Falls back to the upstream version for packages
 * built before fpk_version existed.
 */
export const installedVersionLabel = (app: AppInfo): string =>
  app.installed_fpk_version || app.installed_version;

/** The version an update would move the app TO. */
/**
 * 下载量展示回退链：全局 download_count（fnos-apps 官方/源提供）→
 * 本机安装次数（第三方源应用无全局数据，官方规范不统计外部源下载量）→ 版本。
 */
export const appDownloadLabel = (app: AppInfo): string | null => {
  if (app.download_count != null && app.download_count > 0) {
    return formatCount(app.download_count) + ' 次下载';
  }
  if (app.local_installs != null && app.local_installs > 0) {
    return `本机 ${app.local_installs} 次`;
  }
  return null;
};

export const availableVersionLabel = (app: AppInfo): string =>
  app.available_version || app.latest_version;

/**
 * 已安装应用的"打开"目标 URL（等价于 fnOS 应用中心的"打开"按钮）。
 * daemon 通常不带 host（实测 host 恒为空），此时按当前访问 store 的主机拼接：
 * 用户从 http://<nas>:38011 直达，或在 fnOS Web UI 内嵌 iframe 使用，
 * 两种情况下 location.hostname 都是 NAS 主机，拼出的地址一致。
 * 无 Web 入口的应用返回 null（不渲染"打开"按钮）。
 */
export const appWebUrl = (app: AppInfo): string | null => {
  if (!app.installed) return null;
  // daemon 的 host 字段为空（=当前访问主机）：后端只下发协议/端口/path
  // 结构化字段，主机按当前访问上下文重建——http 直连 / https 反代 /
  // fnOS 桌面壳（fn connect）三种访问方式下都指向用户实际所在的主机。
  // 历史坑：旧版后端拼了字面量 "${host}" 进 web_url，浏览器按字面量
  // 解析 → 打开按钮 ERR_NAME_NOT_RESOLVED（deepseek-harness 实测）。
  if (app.web_url && !app.web_url.includes('${')) return app.web_url;
  if (app.web_port) {
    const protocol = app.web_protocol || (window.location.protocol === 'https:' ? 'https' : 'http');
    return `${protocol}://${window.location.hostname}:${app.web_port}${app.web_path || '/'}`;
  }
  // 无端口的入口由 fnOS Web UI 网关自身服务（/app/xxx、/cgi/xxx、/vm 等），
  // 与 Moo 同一网关：直接用当前 origin 拼 path（比写死 5666 通用，
  // https 反代 / 自定义端口下也能走通）。
  if (app.web_path) {
    return `${window.location.origin}${app.web_path}`;
  }
  return null;
};

/**
 * hostname 是否为「内网本地」身份。
 * 私网 IP（10.x / 192.168.x / 172.16-31.x / 169.254.x）、回环、
 * mDNS（.local / .localhost）或 .home.arpa 视为内网；其余一律视为公网。
 */
const isLanHost = (h: string): boolean => {
  if (h === 'localhost') return true;
  if (h.endsWith('.local') || h.endsWith('.localhost') || h.endsWith('.home.arpa')) return true;
  const m = h.match(/^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/);
  if (!m) return false;
  const a = Number(m[1]);
  const b = Number(m[2]);
  return (
    a === 10 ||
    a === 127 ||
    (a === 169 && b === 254) ||
    (a === 172 && b >= 16 && b <= 31) ||
    (a === 192 && b === 168)
  );
};

/**
 * 当前是否「公网上下文」访问（非内网直达）。
 *
 * 反向白名单判定，覆盖所有公网入口：FN Connect（*.fnos.net）、
 * 公网 IP 直连、自建 DDNS/fn-knock 自定义域名（如 office.app.5ddd.com，
 * 飞牛 app 公网入口实测即此类，每次会话端口还可能不同）。
 * 公网入口通常只暴露 Web 网关端口，不暴露应用端口 → 端口型 URL 不能直开。
 * 内网 IP / mDNS 访问永不命中 → 内网行为零变化。
 */
export const isPublicAccessContext = (): boolean =>
  !isLanHost(window.location.hostname.toLowerCase());

/**
 * 是否运行在飞牛官方 App（FNOS App）的 WebView 内。
 * App WebView 的 UA 带 FNOS/<版本> FNAppType/<Android|iOS> 标识
 * （nginx 访问日志实测：`... FNOS/1.2.0701 FNAppType/Android FNAppVer/1.36.2`）。
 *
 * 行为差异：App 为每个应用隔离 WebView 会话——`window.open` 新开的
 * 上下文不携带当前会话 cookie，跳飞牛主页会 302 到登录页；
 * 而当前 WebView 自身持有有效会话（网关 /app/moo/ 能 200 为证），
 * 同页跳转不会掉登录。浏览器无此问题，维持新标签页。
 */
export const isFnOSAppWebview = (): boolean =>
  /FNAppType\/|FNOS\/\d/.test(navigator.userAgent);

/**
 * 公网上下文下，appWebUrl 拼出的 URL 是否真实可达：
 *
 * - 端口型（web_port 拼当前 host）→ 公网入口不转发应用端口
 *   （实测 check.fnos.net:3000/5701/18090 等全 000），平台按 fnDomain
 *   子域名转发但清单（appcgi.netsvr.domain.list）不开放给第三方应用
 *   → 一律不可达，调用方引导用户回飞牛主页打开
 * - 显式 web_url 指向当前 host / *.fnos.net → 可达
 * - 显式 web_url 指向其他地址（如 NAS 局域网 IP 的 VM 服务）→ 公网不可达
 * - 网关 path（同 origin，如 /app/xxx）→ 可达（与 Moo 同一会话）
 */
export const urlReachableInPublicContext = (app: AppInfo, url: string): boolean => {
  if (app.web_port && !app.web_url) return false;
  try {
    const h = new URL(url).hostname.toLowerCase();
    if (h === window.location.hostname.toLowerCase()) return true;
    return h.endsWith('fnos.net');
  } catch {
    return false;
  }
};

export interface AppsResponse {
  apps: AppInfo[];
  last_check: string;
  /** false on fnOS builds where an in-store update would destroy the app. */
  upgrade_allowed?: boolean;
  upgrade_blocked_reason?: string;
}

export interface RecommendedApp {
  name: string;
  display_name: string;
  description: string;
  source_url: string;
  github_repo?: string;
  latest_version?: string;
  updated_at?: string;
}

export interface RecommendedAppsResponse {
  apps: RecommendedApp[];
}

export interface CheckResponse {
  status: string;
  checked: number;
  updates_available: number;
}

export interface UpdateProgress {
  type?: string;
  step: string;
  progress?: number;
  message?: string;
  new_version?: string;
  app?: string;
  error?: string;
  speed?: number;
  downloaded?: number;
  total?: number;
}

export interface AppOperation {
  step: string;
  progress: number;
  message: string;
  cancel?: () => void;
  speed?: number;
  downloaded?: number;
  total?: number;
}

export const fetchApps = async (): Promise<AppsResponse> => {
  const response = await apiFetch(apiUrl('/api/apps'));
  if (!response.ok) {
    throw new Error(`Failed to fetch apps: ${response.statusText}`);
  }
  return response.json();
};

/**
 * 单应用完整详情。列表载荷为瘦身省略了 changelog/homepage/release_url/
 * sha256 与外部源 icon_url，详情弹窗打开后调本接口补齐（LAN 内几 KB，瞬时）。
 */
export const fetchAppDetail = async (key: string): Promise<AppInfo> => {
  const response = await apiFetch(apiUrl(`/api/apps/${encodeURIComponent(key)}`));
  if (!response.ok) {
    throw new Error(`Failed to fetch app detail: ${response.statusText}`);
  }
  return response.json();
};

export const fetchRecommended = async (): Promise<RecommendedAppsResponse> => {
  const response = await apiFetch(apiUrl('/api/recommended'));
  if (!response.ok) {
    return { apps: [] };
  }
  return response.json();
};

export const triggerCheck = async (): Promise<CheckResponse> => {
  const response = await apiFetch(apiUrl('/api/check'), {
    method: 'POST',
  });
  if (!response.ok) {
    throw new Error(`Failed to trigger check: ${response.statusText}`);
  }
  return response.json();
};

export type SSECallback = (event: UpdateProgress) => void;

export interface SSEHandle {
  promise: Promise<void>;
  cancel: () => void;
}

/**
 * Stream a POST endpoint's SSE body.
 *
 * `url` must already be mount-point resolved by the caller (`apiUrl(...)`).
 * Resolving it a second time here would double-apply the prefix and produce
 * `/store/store/api/...` behind a sub-path proxy.
 */
function streamSSE(url: string, onEvent: SSECallback): SSEHandle {
  const controller = new AbortController();

  const promise = (async () => {
    const response = await apiFetch(url, { method: 'POST', signal: controller.signal });
    if (!response.ok) {
      // 后端错误体是 JSON {error: "..."}（如"应用已安装"），优先展示具体原因。
      let detail = `Request failed: ${response.statusText}`;
      try {
        const j = await response.json();
        if (j && (j.error || j.message)) detail = j.error || j.message;
      } catch { /* 非 JSON 错误体，沿用 statusText */ }
      throw new Error(detail);
    }

    const reader = response.body?.getReader();
    if (!reader) {
      throw new Error('No response body');
    }

    const decoder = new TextDecoder();
    let buffer = '';
    let pendingData = '';
    // 0.6.261：终态事件（done/error）= 操作结束，promise 立即结算——
    // 面板网关（nginx→unix socket）在上游关闭后可能继续持有下游连接，
    // 只等流 EOF 会让 UI 永远卡在「安装中…」蓝条（已安装状态永不翻转）。
    // error 事件同时让 promise 拒绝（此前 error 后 EOF 仍走 resolve，
    // 消费方会误报「安装成功」）。
    // ref 对象承载：闭包内赋值不会触发外层控制流收窄成 never。
    const terminalRef: { current: { kind: 'done' } | { kind: 'error'; err: Error } | null } = { current: null };

    const dispatchPending = () => {
      if (!pendingData) return;
      let parsed: unknown = null;
      try {
        parsed = JSON.parse(pendingData);
      } catch (e) {
        // Don't silently drop terminal events ('done' / 'error') -- log so
        // we can debug a UI stuck in a spinner. Truncate the raw payload to
        // avoid leaking large/sensitive data into the browser console.
        const preview = pendingData.length > 200
          ? pendingData.slice(0, 200) + `...(+${pendingData.length - 200} chars)`
          : pendingData;
        console.warn('streamSSE: failed to parse event payload', e, 'preview:', preview);
      }
      pendingData = '';
      if (parsed == null) return;
      onEvent(parsed as UpdateProgress);
      const ev = parsed as { step?: string; error?: string };
      if (ev.step === 'done') {
        terminalRef.current = { kind: 'done' };
      } else if (ev.step === 'error' || ev.error) {
        terminalRef.current = { kind: 'error', err: new Error(typeof ev.error === 'string' && ev.error ? ev.error : '操作失败') };
      }
    };

    try {
      while (true) {
        if (terminalRef.current) break;
        const { done, value } = await reader.read();
        if (done) break;

        buffer += decoder.decode(value, { stream: true });
        const lines = buffer.split('\n');
        buffer = lines.pop() || '';

        for (const rawLine of lines) {
          // Trim trailing CR so CRLF-style streams (some proxies/servers) parse correctly.
          const line = rawLine.endsWith('\r') ? rawLine.slice(0, -1) : rawLine;
          if (line.startsWith('data: ')) {
            // SSE spec: multiple consecutive data: lines are joined with newline.
            pendingData += (pendingData ? '\n' : '') + line.slice(6);
          } else if (line === '' && pendingData) {
            dispatchPending();
            if (terminalRef.current) break;
          }
        }
      }
      // EOF flush: if the stream ends after a 'data:' line but BEFORE the
      // blank-line terminator (e.g. server killed mid-event), dispatch what
      // we have. Without this, the final 'done' / 'error' event can be lost,
      // leaving the UI stuck on a spinner.
      buffer += decoder.decode();
      if (buffer) {
        const tail = buffer.endsWith('\r') ? buffer.slice(0, -1) : buffer;
        if (tail.startsWith('data: ')) {
          pendingData += (pendingData ? '\n' : '') + tail.slice(6);
        }
      }
      dispatchPending();
    } finally {
      reader.releaseLock();
    }
    const terminal = terminalRef.current;
    if (terminal) {
      // 终态已送达：主动断开连接（网关可能继续持有），再按语义结算。
      controller.abort();
      if (terminal.kind === 'error') throw terminal.err;
    }
  })();

  return { promise, cancel: () => controller.abort() };
}

/** One answer to an app's install wizard. */
export interface WizardParam {
  key: string;
  value: string;
}

/** An app's install-time form, as the app itself declares it. */
export interface AppWizard {
  appname: string;
  version?: string;
  has_wizard: boolean;
  /** Raw fnOS wizard definition; rendered as-is so new field types keep working. */
  content?: WizardStep[];
  install_volume_id?: number;
  error?: string;
}

export interface WizardStep {
  stepTitle?: string;
  items?: WizardItem[];
}

export interface WizardItem {
  type: string;
  field?: string;
  label?: string;
  helpText?: string;
  initValue?: string;
  rules?: { required?: boolean; message?: string; min?: number }[];
}

export const fetchWizard = async (appname: string): Promise<AppWizard> => {
  const r = await apiFetch(apiUrl(`/api/apps/${appname}/wizard`));
  if (!r.ok) return { appname, has_wizard: false };
  return r.json();
};

/** 官方应用中心（fnos-official）安装参数：安装卷 + 用户对每个依赖的选择。 */
export interface PanelInstallParams {
  volumeID?: number;
  deps?: { appName: string; action: 'install' | 'skip' }[];
}

export const installApp = (appname: string, onEvent: SSECallback, wizard?: WizardParam[], panel?: PanelInstallParams): SSEHandle => {
  const params = new URLSearchParams();
  if (wizard && wizard.length) params.set('wizard', JSON.stringify(wizard));
  if (panel) params.set('panel', JSON.stringify(panel));
  const qs = params.toString() ? `?${params.toString()}` : '';
  return streamSSE(apiUrl(`/api/apps/${appname}/install${qs}`), onEvent);
};

/** 「下载 fpk」：后端按镜像链下载 FPK 到本地缓存，并登记进面板官方下载通道（SSE 进度）。 */
export const downloadFpk = (appname: string, onEvent: SSECallback): SSEHandle =>
  streamSSE(apiUrl(`/api/apps/${appname}/download-task`), onEvent);

/** 官方应用详情页 + 依赖弹窗数据（面板实时状态 + 商店目录同名条目）。 */
export interface PanelDep {
  sourceID: string;
  appName: string;
  name: string;
  icon: string;
  version: string;
  /** noinstall / nostart / running（面板实时状态）。 */
  status: string;
}

export interface PanelDetailApp {
  appName: string;
  name: string;
  version: string;
  icon: string;
  docker: boolean;
  installDepApps: PanelDep[];
  appDetail: {
    desc?: string;
    maintainer?: string;
    maintainerUrl?: string;
    distributor?: string;
    distributorUrl?: string;
    installSize?: number;
    osMinVersion?: string;
    /** 官方详情页预览截图 URL（部分应用为空）。 */
    poster?: string[];
  };
}

export interface PanelDetailResponse {
  app: PanelDetailApp;
  volume: number;
  /** depAppname -> 商店目录里同名应用展示标签（可「用已有的」）。 */
  same_name_apps?: Record<string, string[]>;
}

export const fetchPanelDetail = async (appname: string): Promise<PanelDetailResponse> => {
  const r = await apiFetch(apiUrl(`/api/apps/${encodeURIComponent(appname)}/panel-detail`));
  if (!r.ok) {
    const body = await r.json().catch(() => null);
    throw new Error(body?.error || `获取官方应用详情失败: ${r.statusText}`);
  }
  return r.json();
};

// 官方详情前端会话缓存：同一 SPA 会话内重复打开官方应用详情，
// 描述/预览图/发布者/体积 10 分钟内不再发请求（配合服务端 detailCache，
// 二次打开详情 bodyReady 只剩 fetchAppDetail 一个快请求，不再「卡一下」）。
// 安装/更新完成后的强制重拉走 fetchPanelDetail（绕过缓存拿最新）。
const panelDetailCache = new Map<string, { at: number; data: PanelDetailResponse }>();
const PANEL_DETAIL_TTL_MS = 10 * 60 * 1000;
export const fetchPanelDetailCached = async (appname: string): Promise<PanelDetailResponse> => {
  const hit = panelDetailCache.get(appname);
  if (hit && Date.now() - hit.at < PANEL_DETAIL_TTL_MS) return hit.data;
  const data = await fetchPanelDetail(appname);
  panelDetailCache.set(appname, { at: Date.now(), data });
  return data;
};

/**
 * 已安装但无源元数据的应用（自装 FPK/系统自带）：从面板补详情
 * （描述/开发者/截图）。拿不到返回 null（详情页用列表条目兜底渲染）。
 */
export const fetchInstalledDetail = async (appname: string): Promise<PanelDetailResponse | null> => {
  try {
    const r = await apiFetch(apiUrl(`/api/installed-detail?appname=${encodeURIComponent(appname)}`));
    if (!r.ok) return null;
    const d = await r.json();
    if (!d?.app) return null;
    return { app: d.app, volume: 0 };
  } catch {
    return null;
  }
};

// 0.6.255：testPanelLogin（POST /api/panel/test）已随面板账号一并移除。
// 官方源连接见 officialAuthorize / officialAuthorizeHeadless（OAuth）。

export const updateApp = (appname: string, onEvent: SSECallback): SSEHandle => {
  return streamSSE(apiUrl(`/api/apps/${appname}/update`), onEvent);
};

export const uninstallApp = (appname: string, onEvent: SSECallback): SSEHandle => {
  return streamSSE(apiUrl(`/api/apps/${appname}/uninstall`), onEvent);
};

// 启动 / 停用已安装应用（与 fnOS 应用中心同步）
export const controlApp = async (appname: string, action: 'start' | 'stop'): Promise<void> => {
  const response = await apiFetch(apiUrl(`/api/apps/${encodeURIComponent(appname)}/${action}`), { method: 'POST' });
  if (!response.ok) {
    const body = await response.json().catch(() => null);
    throw new Error(body?.error || `${action === 'start' ? '启动' : '停用'}失败: ${response.statusText}`);
  }
};

export const reloadApps = (onEvent: SSECallback): SSEHandle => {
  return streamSSE(apiUrl('/api/apps/reload'), onEvent);
};

export interface MirrorOption {
  key: string;
  label: string;
  description: string;
}

export interface VolumeOption {
  index: number;
  path: string;
  total_bytes: number;
  free_bytes: number;
}

export interface Settings {
  check_interval_hours: number;
  mirror: string;
  mirror_options?: MirrorOption[];
  docker_mirror: string;
  docker_mirror_options?: MirrorOption[];
  custom_github_mirror?: string;
  custom_docker_mirror?: string;
  install_volume: number;
  volume_options?: VolumeOption[];
  // 内置源列表自动同步（空/缺省 = 内置默认列表地址）
  source_list_url?: string;
  source_list_disabled?: boolean;
  // FPK 下载目录 + 应用源自动监测（缺省 = 目录默认 / 监测开启）
  download_dir?: string;
  source_auto_care_disabled?: boolean;
  // 自动更新应用（周期检查发现更新时后台自动安装，无需打开应用）
  auto_update?: boolean;
  // 目录语言（0.6.269）：auto（默认，跟随浏览器语言）/ zh-CN / en-US
  catalog_language?: string;
  // 0.6.255：面板账号字段（panel_enabled/panel_username/panel_base_url/
  // panel_has_password/panel_decrypt_failed）已从设置中彻底移除——官方源
  // 改为纯 OAuth，授权时临时输入面板账号（不落地存储）。
  // 备份设置（备份设置 tab）
  backup_dir?: string;
  backup_auto?: boolean;
  backup_interval_days?: number;
  cache_clean_days?: number;
  cache_clean_every_days?: number;
  // 日志页显示行数（0.6.261；缺省 = 默认 200）
  log_lines?: number;
  // 加速源自动测速间隔（0.6.148 齿轮选择框）：0h0m = 未设置（后端按 5 分钟）
  gh_probe_hours?: number;
  gh_probe_minutes?: number;
  dk_probe_hours?: number;
  dk_probe_minutes?: number;
  // 科学加速（0.6.206）：本机代理（仅 GitHub 域名改道）
  proxy_enabled?: boolean;
  proxy_url?: string;
  // Dock 主导航 / 设置 tab 当前生效顺序（0.6.122；缺省 = 默认顺序）
  dock_order?: string[];
  settings_tab_order?: string[];
}

/** 一个备份文件（config.json 全量快照）。 */
export interface BackupEntry {
  name: string;
  size: number;
  mod_at: string;
  created?: string;
}

/** Moo 应用缓存统计（图标/README 等；已下载 FPK 不在其列）。 */
export interface AppCacheStats {
  dir: string;
  file_count: number;
  total_bytes: number;
  old_count: number;
  old_bytes: number;
}

/** GET /api/backups 响应：备份列表 + 备份/清理设置 + 应用缓存统计。 */
export interface BackupsResponse {
  dir: string;
  auto: boolean;
  interval_days: number;
  last_backup_at?: string;
  files: BackupEntry[];
  app_cache: AppCacheStats;
  cache_clean_days: number;
  cache_clean_every_days: number;
}

export interface SourceListSyncResult {
  fetched: number;
  already: number;
  added: number;
  failed: number;
  added_names?: string[];
  errors?: string[];
}

export interface MirrorCheckResult {
  key: string;
  label: string;
  latency_ms: number;
  /** 吞吐测速（字节/秒，仅 GitHub 文件加速源） */
  speed_bps?: number;
  status: 'ok' | 'timeout' | 'error';
}

export interface MirrorCheckResponse {
  github_mirrors: MirrorCheckResult[];
  docker_mirrors: MirrorCheckResult[];
}

export const checkMirrors = async (type?: 'github' | 'docker'): Promise<MirrorCheckResponse> => {
  const params = type ? `?type=${type}` : '';
  const response = await apiFetch(apiUrl(`/api/mirrors/check${params}`), { method: 'POST' });
  if (!response.ok) {
    throw new Error(`Failed to check mirrors: ${response.statusText}`);
  }
  return response.json();
};

/** 单个加速源的健康状态（来自后台周期探测 + 手动测速，服务端汇总）。 */
export interface MirrorStat {
  key: string;
  label: string;
  /** 首字节延迟（ms）；GitHub 源体检以吞吐为准，此值仅参考 */
  latency_ms: number;
  /** 吞吐测速（字节/秒，仅 GitHub 文件加速源；0 = 未测出） */
  speed_bps?: number;
  status: 'ok' | 'fail' | '';
  last_check: string;
  consec_fails: number;
}

export interface MirrorSwitchInfo {
  from: string;
  to: string;
  time: string;
  reason: string;
}

/** GitHub 加速源健康监测快照（智能监测 + 自动切换）。 */
export interface MirrorHealth {
  mirrors: MirrorStat[];
  /** 用户在设置里选定的镜像 key（auto = 智能模式） */
  selected: string;
  /** 当前实际生效的镜像 key（auto 时 = 最稳定源） */
  active: string;
  last_probe: string;
  last_switch?: MirrorSwitchInfo | null;
  interval_s: number;
}

export const fetchMirrorHealth = async (refresh = false): Promise<MirrorHealth> => {
  const params = refresh ? '?refresh=1' : '';
  const response = await apiFetch(apiUrl(`/api/mirrors/health${params}`));
  if (!response.ok) {
    throw new Error(`Failed to fetch mirror health: ${response.statusText}`);
  }
  return response.json();
};

/** Docker 镜像加速健康监测快照（与 GitHub 版同构）。 */
export const fetchDockerMirrorHealth = async (refresh = false): Promise<MirrorHealth> => {
  const params = refresh ? '?refresh=1' : '';
  const response = await apiFetch(apiUrl(`/api/mirrors/docker/health${params}`));
  if (!response.ok) {
    throw new Error(`Failed to fetch docker mirror health: ${response.statusText}`);
  }
  return response.json();
};


// ── 外部应用源（FnDepot V1/V2 协议）──────────────────────────────────────────

export interface SourceEntry {
  id: string;
  name: string;
  url: string;
  author?: string;
  homepage?: string;
  app_count: number;
  error?: string;
  last_fetched?: string;
  /** 源是否启用（关闭后不再抓取）；内置官方源恒为 true */
  enabled?: boolean;
  /** 连续「抓取失败或 0 应用」次数（自动监测：连续 5 次自动关闭） */
  empty_streak?: number;
  /** 是否关注（星标，0.6.143）；关注源新增应用时推通知 */
  favorite?: boolean;
}

export interface SourcesResponse {
  sources: SourceEntry[];
}

/** 0.6.198 搜索源链接：归一化 key → 源名集合（公开端点，不含原始 URL） */
export interface SearchSourceKeyEntry {
  key: string;
  names: string[];
}

export const fetchSearchSourceKeys = async (): Promise<SearchSourceKeyEntry[]> => {
  const response = await apiFetch(apiUrl('/api/search/source-keys'));
  if (!response.ok) {
    throw new Error(`Failed to fetch source keys: ${response.statusText}`);
  }
  return response.json();
};

const extractError = async (response: Response, fallback: string): Promise<string> => {
  try {
    const body = await response.json();
    if (body && body.error) return body.error;
  } catch {
    // 非 JSON 错误体，用 fallback
  }
  return fallback;
};

export const fetchSources = async (): Promise<SourcesResponse> => {
  const response = await apiFetch(apiUrl('/api/sources'));
  if (!response.ok) {
    throw new Error(await extractError(response, `获取应用源列表失败: ${response.statusText}`));
  }
  return response.json();
};

/** 添加外部应用源。后端会立即抓取+解析验证，可能耗时数秒。 */
export const addSource = async (url: string, name?: string): Promise<SourceEntry> => {
  const response = await apiFetch(apiUrl('/api/sources'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ url, name: name || undefined }),
  });
  if (!response.ok) {
    throw new Error(await extractError(response, `添加应用源失败: ${response.statusText}`));
  }
  const body = await response.json();
  return body.source as SourceEntry;
};

export interface BatchSourceResult {
  url: string;
  ok: boolean;
  name?: string;
  /** 0.6.173：与列表已有源/本批已加源同地址，跳过（只保留一个），非错误 */
  deduped?: boolean;
  error?: string;
}

/** 批量添加外部应用源（多行输入一次提交）。单条失败不影响其他条。 */
export const addSourcesBatch = async (
  items: { url: string; name?: string }[],
): Promise<{ added: number; deduped: number; results: BatchSourceResult[] }> => {
  const response = await apiFetch(apiUrl('/api/sources/batch'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ items }),
  });
  if (!response.ok) {
    throw new Error(await extractError(response, `批量添加应用源失败: ${response.statusText}`));
  }
  return response.json();
};

/** 手动同步单个外部源（立即抓取，返回最新应用数）。 */
export const syncSource = async (id: string): Promise<SourceEntry> => {
  const response = await apiFetch(apiUrl(`/api/sources/${encodeURIComponent(id)}/sync`), {
    method: 'POST',
  });
  if (!response.ok) {
    throw new Error(await extractError(response, `同步应用源失败: ${response.statusText}`));
  }
  const body = await response.json();
  return body.source as SourceEntry;
};

/** 一键刷新所有应用源（0.6.171，并发 8、只刷已启用源、单源失败不影响其他）。 */
export const syncAllSources = async (): Promise<{
  total: number;
  synced: number;
  failed: { name: string; error: string }[];
}> => {
  const response = await apiFetch(apiUrl('/api/sources/sync-all'), { method: 'POST' });
  if (!response.ok) {
    throw new Error(await extractError(response, `刷新所有应用源失败: ${response.statusText}`));
  }
  return response.json();
};

/** 一键恢复所有默认应用源（0.6.172）：补齐被删/缺失的默认源 +
 *  相同源地址去重（只保留一个，官方源永不被删）。 */
export const restoreDefaultSources = async (): Promise<{
  fetched: number;
  restored: number;
  already: number;
  failed: number;
  deduped: number;
  restored_names?: string[];
  removed_names?: string[];
  errors?: string[];
}> => {
  const response = await apiFetch(apiUrl('/api/sources/restore-defaults'), { method: 'POST' });
  if (!response.ok) {
    throw new Error(await extractError(response, `恢复默认应用源失败: ${response.statusText}`));
  }
  return response.json();
};

/** 同步内置源列表：自动发现并添加列表中未添加过的 FnDepot 应用源。 */
export const syncSourceList = async (): Promise<SourceListSyncResult> => {
  const response = await apiFetch(apiUrl('/api/sources/sync-list'), { method: 'POST' });
  if (!response.ok) {
    throw new Error(await extractError(response, `同步源列表失败: ${response.statusText}`));
  }
  return response.json();
};

/** 从同机 New Store（fnos-apps-store）同步全部应用源（含 0 应用源；只增不删）。 */
export const removeSource = async (id: string): Promise<void> => {
  const response = await apiFetch(apiUrl(`/api/sources/${encodeURIComponent(id)}`), {
    method: 'DELETE',
  });
  if (!response.ok) {
    throw new Error(await extractError(response, `删除应用源失败: ${response.statusText}`));
  }
};

/** 开启/关闭应用源（关闭后不再抓取，其应用从目录移除）。 */
/** 重命名应用源（应用列表的源徽章自动跟随）。 */
export const renameSource = async (id: string, name: string): Promise<void> => {
  const response = await apiFetch(apiUrl(`/api/sources/${encodeURIComponent(id)}/rename`), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name }),
  });
  if (!response.ok) {
    throw new Error(await extractError(response, `重命名应用源失败: ${response.statusText}`));
  }
};

/** 关注/取消关注应用源（0.6.143 源行星标）。关注源新增应用时推通知。 */
export const toggleSourceFavorite = async (id: string, favorite: boolean): Promise<void> => {
  const response = await apiFetch(apiUrl(`/api/sources/${encodeURIComponent(id)}/favorite`), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ favorite }),
  });
  if (!response.ok) {
    throw new Error(await extractError(response, `更新源关注失败: ${response.statusText}`));
  }
};

export const toggleSource = async (id: string, enabled: boolean): Promise<void> => {
  const response = await apiFetch(apiUrl(`/api/sources/${encodeURIComponent(id)}/toggle`), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ enabled }),
  });
  if (!response.ok) {
    throw new Error(await extractError(response, `切换应用源状态失败: ${response.statusText}`));
  }
};

/** 已下载的 FPK 缓存条目。 */
export interface FpkDownloadFile {
  name: string;
  size: number;
  mod_at: string;
  /** FPK 清单里的 appname（解析失败为空）。 */
  appname?: string;
  /** 包 manifest 的 display_name（中文名/正式名；后台索引未就绪时为空）。 */
  display_name?: string;
  /** 该应用当前是否已安装（含官方中心/其他方式安装）。 */
  installed?: boolean;
}

/**
 * 应用源显示名：官方应用中心 → 中文，内置目录（原 fnos-store 源）→ "fnos-store"，
 * 外部 FnDepot 源用其显示名。搜索/筛选/徽章统一走这里。
 */
export const sourceLabel = (app: AppInfo): string => {
  switch (app.source) {
    case 'fnos-official': return '飞牛应用中心源';
    case 'fnos-apps': return 'fnos-store';
    default: return app.source || '';
  }
};

/** 开发者显示名：内置目录（fnos-apps）的应用统一显示为 conversun。 */
export const effectiveMaintainer = (app: AppInfo): string =>
  app.maintainer || (app.source === 'fnos-apps' ? 'conversun' : '');

/**
 * 把可能混有 HTML 片段的描述文本转成纯文本（列表卡/行列表按纯文本渲染）。
 * 部分官方应用 desc 里带 <h1>/<p>/<strong> 等标签，直接插会露出标签；
 * 去标签 + 解码常见实体 + 折叠空白。
 */
export const descriptionPlainText = (t: string): string => {
  if (!t) return '';
  let s = t;
  if (/<[a-z][\s\S]*?>/i.test(s)) {
    s = s
      .replace(/<\s*br\s*\/?\s*>/gi, '\n')
      .replace(/<\s*\/\s*(p|div|li|h[1-6]|tr|blockquote)\s*>/gi, '\n')
      .replace(/<[^>]+>/g, ' ');
  }
  s = s
    .replace(/&nbsp;/gi, ' ')
    .replace(/&amp;/gi, '&')
    .replace(/&lt;/gi, '<')
    .replace(/&gt;/gi, '>')
    .replace(/&quot;/gi, '"')
    .replace(/&#39;/gi, "'")
    .replace(/[ \t]+\n/g, '\n')
    .replace(/\n{2,}/g, '\n');
  return s.trim();
};

/** FPK 下载目录 + 已下载列表（设置页「FPK 下载目录」同步展示）。 */
export const fetchFpkDownloads = async (): Promise<{ dir: string; files: FpkDownloadFile[] }> => {
  const response = await apiFetch(apiUrl('/api/fpk-downloads'));
  if (!response.ok) {
    throw new Error(await extractError(response, `获取 FPK 下载列表失败: ${response.statusText}`));
  }
  return response.json();
};

/** 删除单个已下载 FPK 缓存。 */
export const deleteFpkDownload = async (name: string): Promise<void> => {
  const response = await apiFetch(apiUrl(`/api/fpk-downloads/${encodeURIComponent(name)}`), {
    method: 'DELETE',
  });
  if (!response.ok) {
    throw new Error(await extractError(response, `删除 FPK 缓存失败: ${response.statusText}`));
  }
};

/** 直接安装已下载的 FPK 缓存（SSE 进度流；不重新下载，文件保留在缓存中）。 */
export const installFpkDownload = (name: string, onEvent: SSECallback, wizard?: WizardParam[]): SSEHandle => {
  const qs = wizard?.length ? `?wizard=${encodeURIComponent(JSON.stringify(wizard))}` : '';
  return streamSSE(apiUrl(`/api/fpk-downloads/${encodeURIComponent(name)}/install${qs}`), onEvent);
};

/** Install wizard definition for a downloaded FPK (same shape as app source wizards). */
export const fetchFpkDownloadWizard = async (name: string): Promise<AppWizard> => {
  const response = await apiFetch(apiUrl(`/api/fpk-downloads/${encodeURIComponent(name)}/wizard`));
  if (!response.ok) throw new Error('获取安装向导失败');
  return response.json() as Promise<AppWizard>;
};

/** 手动排序应用源（传全部自定义源 ID 的新顺序；官方源固定置顶不受影响）。 */
export const reorderSources = async (order: string[]): Promise<void> => {
  const response = await apiFetch(apiUrl('/api/sources/reorder'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ order }),
  });
  if (!response.ok) {
    throw new Error(await extractError(response, `排序应用源失败: ${response.statusText}`));
  }
};

/** 应用详情页资源（README / 预览图 / README 内嵌图片代理）的代理地址，走后端镜像链。 */
export const assetUrl = (appname: string, type: 'readme' | 'preview' | 'readme-img', index?: number, imgUrl?: string): string =>
  apiUrl(`/api/apps/${encodeURIComponent(appname)}/asset?type=${type}${index != null ? `&index=${index}` : ''}${type === 'readme-img' && imgUrl ? `&u=${encodeURIComponent(imgUrl)}` : ''}`);

/**
 * README 内嵌图片改写：github 系 host 的图直连在国内浏览器常慢/挂，
 * 改走后端 readme-img 代理（镜像竞速 + 7 天缓存）。其余 host
 * （shields/juejin 等）浏览器直连更快，不动。
 */
const PROXY_IMG_HOSTS = new Set([
  'raw.githubusercontent.com',
  'github.com',
  'github.githubassets.com',
  'user-images.githubusercontent.com',
  'camo.githubusercontent.com',
  'codeload.github.com',
  'objects.githubusercontent.com',
]);

export const rewriteReadmeImgSrc = (src: string | undefined | null, appKey: string): string => {
  if (!src) return src || '';
  try {
    const u = new URL(src, window.location.origin);
    if (u.protocol === 'https:' || u.protocol === 'http:') {
      if (PROXY_IMG_HOSTS.has(u.hostname)) {
        return assetUrl(appKey, 'readme-img', undefined, u.toString());
      }
    }
  } catch {
    /* 非法 URL 原样返回 */
  }
  return src;
};

export interface StatusResponse {
  version?: string;
  platform: string;
}

export interface StoreUpdateInfo {
  current_version: string;
  available_version?: string;
  has_update: boolean;
}

export const fetchSettings = async (): Promise<Settings> => {
  const response = await apiFetch(apiUrl('/api/settings'));
  if (!response.ok) {
    throw new Error(`Failed to fetch settings: ${response.statusText}`);
  }
  return response.json();
};

// 后台任务（安装/更新/下载在客户端断开后继续跑；轮询它看进度）
export interface BackgroundTask {
  /** 任务唯一 ID（下载任务）；同一应用多次下载是不同的 id。 */
  id?: string;
  appname: string;
  op: string; // "install" | "update" | "download"
  status: string; // "queued" | "running" | "paused" | "done" | "failed"
  step?: string;
  progress?: number;
  message?: string;
  new_version?: string;
  downloaded?: number;
  total?: number;
  speed?: number;
}

export const fetchTasks = async (): Promise<BackgroundTask[]> => {
  const response = await apiFetch(apiUrl('/api/tasks'));
  if (!response.ok) {
    throw new Error(`Failed to fetch tasks: ${response.statusText}`);
  }
  return response.json();
};

/** 清除已完成/失败/暂停的下载任务（运行中会 400；已写文件保留在下载目录）。 */
export const clearDownloadTask = async (id: string): Promise<void> => {
  const response = await apiFetch(apiUrl(`/api/tasks/${encodeURIComponent(id)}`), {
    method: 'DELETE',
  });
  if (!response.ok) {
    let msg = response.statusText;
    try {
      const j = await response.json();
      if (j?.error) msg = j.error;
    } catch { /* 忽略解析失败 */ }
    throw new Error(msg);
  }
};

/** 暂停进行中的 FPK 下载（.part 保留，可继续；释放该应用的队列槽位）。 */
export const pauseDownload = async (appname: string): Promise<void> => {
  const response = await apiFetch(apiUrl(`/api/apps/${encodeURIComponent(appname)}/task/pause`), {
    method: 'POST',
  });
  if (!response.ok) {
    throw new Error(await extractError(response, `暂停下载失败: ${response.statusText}`));
  }
};

/** 继续暂停的 FPK 下载（Range 续传；立即返回，进度走后台任务）。 */
export const resumeDownload = async (appname: string): Promise<void> => {
  const response = await apiFetch(apiUrl(`/api/apps/${encodeURIComponent(appname)}/task/resume`), {
    method: 'POST',
  });
  if (!response.ok) {
    throw new Error(await extractError(response, `继续下载失败: ${response.statusText}`));
  }
};

// 字段均可选：后端按「缺省不改动」处理（读全量→改单字段→写回），
// 允许局部更新（如只切下载目录 / 只切自动更新开关）。
export const updateSettings = async (settings: { check_interval_hours?: number; mirror?: string; docker_mirror?: string; custom_github_mirror?: string; custom_docker_mirror?: string; install_volume?: number; source_list_url?: string; source_list_disabled?: boolean; download_dir?: string; source_auto_care_disabled?: boolean; auto_update?: boolean; catalog_language?: string; backup_dir?: string | null; backup_auto?: boolean; backup_interval_days?: number; cache_clean_days?: number; cache_clean_every_days?: number; log_lines?: number; gh_probe_hours?: number; gh_probe_minutes?: number; dk_probe_hours?: number; dk_probe_minutes?: number; proxy_enabled?: boolean; proxy_url?: string; dock_order?: string[]; settings_tab_order?: string[] }): Promise<void> => {
  const response = await apiFetch(apiUrl('/api/settings'), {
    method: 'PUT',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(settings),
  });
  if (!response.ok) {
    throw new Error(await extractError(response, `Failed to update settings: ${response.statusText}`));
  }
};

// ── Docker 系统镜像源应用（0.6.269，M4 Docker 优选接入）────────────────
export interface DockerMirrorStatus {
  daemon_json_exists: boolean;
  mirrors: string[];
  docker_active: boolean;
  applied?: boolean;
  error?: string;
  last_applied?: {
    applied_at: string;
    mirror: string;
    mirrors: string[];
    backup_file?: string;
    docker_active: boolean;
  };
}
export const fetchDockerMirrorStatus = async (): Promise<DockerMirrorStatus> => {
  const res = await apiFetch(apiUrl('/api/settings/docker-mirror/status'));
  if (!res.ok) throw new Error(`获取 Docker 镜像源状态失败: ${res.status}`);
  return res.json();
};
export const applyDockerMirror = async (
  mirror: string,
  customUrl: string,
): Promise<{ ok: boolean; message: string; mirrors: string[]; restarted: boolean }> => {
  const res = await apiFetch(apiUrl('/api/settings/docker-mirror/apply'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ mirror, custom_url: customUrl }),
  });
  if (!res.ok) {
    throw new Error(await extractError(res, `应用 Docker 镜像源失败: ${res.status}`));
  }
  return res.json();
};

/** 备份设置：备份列表 + 备份/清理设置 + FPK 缓存统计。 */
export const fetchBackups = async (): Promise<BackupsResponse> => {
  const response = await apiFetch(apiUrl('/api/backups'));
  if (!response.ok) {
    throw new Error(await extractError(response, `Failed to fetch backups: ${response.statusText}`));
  }
  return response.json();
};

// ── 通知设置（0.6.120，形式对齐 fn-knock 事件中心：渠道/规则/记录）──

/** 一类可通知事件（后端目录项）。 */
export interface NotifyEvent {
  key: string;
  label: string;
  desc: string;
  group: string;
  default: boolean;
}

/** GET /api/notify-settings 响应。events 为解析后的开/关（按目录缺省）。 */
export interface NotifySettings {
  enabled: boolean;
  events: Record<string, boolean>;
  catalog: NotifyEvent[];
  mem_alert_mb: number;
  cpu_alert_pct: number;
  /** 通知详情页基础地址（0.6.144，卡片形式跳转用；空 = 未配置）。 */
  view_base?: string;
}

// ── 推送渠道（0.6.121）──

/** 渠道连接参数字段定义（前端动态表单）。 */
export interface ChannelFieldDef {
  key: string;
  label: string;
  placeholder?: string;
  required: boolean;
  sensitive?: boolean;
  default?: string;
}

/** 一类渠道的静态定义。 */
export interface ChannelDef {
  type: string;
  label: string;
  desc: string;
  fields: ChannelFieldDef[];
  supports_markdown: boolean;
}

/** 一个推送渠道（params 为脱敏回显值，"****" 前缀 = 未改动）。 */
export interface NotifyChannel {
  id: string;
  type: string;
  name: string;
  enabled: boolean;
  params: Record<string, string>;
  timeout?: number;
  /** 通知形式（0.6.144，仅 wecom 生效）：""/markdown 默认 | markdown_v2 | card。 */
  format?: string;
  /** 通知信息长度（0.6.175，全部渠道）：""/friendly 友好默认 | concise 简洁 | full 完整详细。 */
  verbosity?: string;
}

export const fetchNotifySettings = async (): Promise<NotifySettings> => {
  const response = await apiFetch(apiUrl('/api/notify-settings'));
  if (!response.ok) {
    throw new Error(await extractError(response, `Failed to fetch notify settings: ${response.statusText}`));
  }
  return response.json();
};

/** GET /api/notify-channels：渠道列表（脱敏）+ 渠道定义。 */
export const fetchNotifyChannels = async (): Promise<{ channels: NotifyChannel[]; definitions: ChannelDef[] }> => {
  const response = await apiFetch(apiUrl('/api/notify-channels'));
  if (!response.ok) {
    throw new Error(await extractError(response, `Failed to fetch notify channels: ${response.statusText}`));
  }
  return response.json();
};

/** POST /api/notify-channels：新增渠道。 */
export const addNotifyChannel = async (ch: Omit<NotifyChannel, 'id'>): Promise<NotifyChannel> => {
  const response = await apiFetch(apiUrl('/api/notify-channels'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(ch),
  });
  if (!response.ok) {
    throw new Error(await extractError(response, `Failed to add notify channel: ${response.statusText}`));
  }
  const data = await response.json();
  return data.channel as NotifyChannel;
};

/** PUT /api/notify-channels/{id}：更新渠道（脱敏参数 = 未改动）。 */
export const updateNotifyChannel = async (id: string, ch: Partial<NotifyChannel>): Promise<NotifyChannel> => {
  const response = await apiFetch(apiUrl(`/api/notify-channels/${id}`), {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(ch),
  });
  if (!response.ok) {
    throw new Error(await extractError(response, `Failed to update notify channel: ${response.statusText}`));
  }
  const data = await response.json();
  return data.channel as NotifyChannel;
};

/** DELETE /api/notify-channels/{id}：删除渠道。 */
export const deleteNotifyChannel = async (id: string): Promise<void> => {
  const response = await apiFetch(apiUrl(`/api/notify-channels/${id}`), { method: 'DELETE' });
  if (!response.ok) {
    throw new Error(await extractError(response, `Failed to delete notify channel: ${response.statusText}`));
  }
};

/** POST /api/notify-channels/test：弹窗内「测试提供商」（0.6.139）——用当前表单值直接测试，不落盘。 */
export const testNotifyChannelDraft = async (
  channel: { id?: string; type: string; name: string; params: Record<string, string>; format?: string },
): Promise<void> => {
  const response = await apiFetch(apiUrl('/api/notify-channels/test'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(channel),
  });
  const data = await response.json().catch(() => ({}));
  if (!response.ok || data.ok === false) {
    throw new Error(data.error || `Failed to test notify channel: ${response.statusText}`);
  }
};

/** POST /api/notify-channels/{id}/test：发送测试消息。 */
export const testNotifyChannel = async (id: string): Promise<void> => {
  const response = await apiFetch(apiUrl(`/api/notify-channels/${id}/test`), { method: 'POST' });
  const data = await response.json().catch(() => ({}));
  if (!response.ok || data.ok === false) {
    throw new Error(data.error || `Failed to test notify channel: ${response.statusText}`);
  }
};

/** POST /api/notify/welcome：手动重发「欢迎使用Moo」欢迎语（0.6.155）。
 *  事件开关关闭时后端静默（sent=false + reason）。 */
export const fireNotifyWelcome = async (): Promise<{ sent: boolean; reason?: string }> => {
  const response = await apiFetch(apiUrl('/api/notify/welcome'), { method: 'POST' });
  const data = await response.json().catch(() => ({}));
  if (!response.ok || data.ok === false) {
    throw new Error(data.error || `Failed to fire welcome notification: ${response.statusText}`);
  }
  return { sent: data.sent !== false, reason: data.reason };
};

/** POST /api/notify/fire：手动触发任意事件通知（0.6.170，通知规则行门铃）。
 *  事件开关关闭时后端静默（sent=false + reason）。 */
export const fireNotifyEvent = async (event: string): Promise<{ sent: boolean; reason?: string }> => {
  const response = await apiFetch(apiUrl('/api/notify/fire'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ event }),
  });
  const data = await response.json().catch(() => ({}));
  if (!response.ok || data.ok === false) {
    throw new Error(data.reason || data.error || `Failed to fire notification: ${response.statusText}`);
  }
  return { sent: data.sent !== false, reason: data.reason };
};

/**
 * PUT /api/notify-settings。enabled / events 均为可选（缺省 = 不改动）；
 * events 为合并语义（只改提交的 key）。返回保存后的完整解析值（事件全集）。
 */
export const updateNotifySettings = async (
  patch: { enabled?: boolean; events?: Record<string, boolean>; view_base?: string },
): Promise<{ enabled: boolean; events: Record<string, boolean> }> => {
  const response = await apiFetch(apiUrl('/api/notify-settings'), {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(patch),
  });
  if (!response.ok) {
    throw new Error(await extractError(response, `Failed to update notify settings: ${response.statusText}`));
  }
  const data = await response.json();
  return { enabled: data.enabled, events: data.events };
};

/** 一条通知记录（时间戳 Unix 秒，新→旧）。 */
export interface NotifyLogEntry {
  ts: number;
  event: string;
  msg: string;
  ok: boolean;
  /** 完整通知正文（0.6.143：应用内记录方格折叠卡展示；外部渠道可能是紧凑版）。 */
  content?: string;
  /** 渠道名 → 发送错误（缺省/空 = 无渠道或全部成功）。 */
  channels?: Record<string, string>;
}

export const fetchNotifyLog = async (): Promise<NotifyLogEntry[]> => {
  const response = await apiFetch(apiUrl('/api/notify-log'));
  if (!response.ok) {
    throw new Error(await extractError(response, `Failed to fetch notify log: ${response.statusText}`));
  }
  const data = await response.json();
  return data.entries || [];
};

/** 前端任务事件上报（后端按开关决定是否入记录；fire-and-forget 用）。 */
export const recordNotifyEvent = async (event: string, msg: string, ok: boolean): Promise<void> => {
  try {
    await apiFetch(apiUrl('/api/notify-log'), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ event, msg, ok }),
    });
  } catch {
    /* 记录失败不干扰主流程 */
  }
};

export const clearNotifyLog = async (): Promise<void> => {
  const response = await apiFetch(apiUrl('/api/notify-log/clear'), { method: 'POST' });
  if (!response.ok) {
    throw new Error(await extractError(response, `Failed to clear notify log: ${response.statusText}`));
  }
};

// ── 关于页 ──

export interface AboutInfo {
  version: string;
  platform: string;
  arch: string;
  go_version?: string;
  port: string;
  started_at: number;
  uptime_s: number;
  app: {
    name: string;
    display_name: string;
    desc: string;
    author: string;
    author_url: string;
    homepage: string;
  };
}

export const fetchAbout = async (): Promise<AboutInfo> => {
  const response = await apiFetch(apiUrl('/api/about'));
  if (!response.ok) {
    throw new Error(await extractError(response, `Failed to fetch about: ${response.statusText}`));
  }
  return response.json();
};

/** 立即写一份设置备份快照。 */
export const runBackupNow = async (): Promise<{ ok: boolean; name: string }> => {
  const response = await apiFetch(apiUrl('/api/backups'), { method: 'POST' });
  if (!response.ok) {
    throw new Error(await extractError(response, `Failed to create backup: ${response.statusText}`));
  }
  return response.json();
};

/** 删除一个备份文件。 */
export const deleteBackup = async (name: string): Promise<void> => {
  const response = await apiFetch(apiUrl(`/api/backups/${encodeURIComponent(name)}`), { method: 'DELETE' });
  if (!response.ok) {
    throw new Error(await extractError(response, `Failed to delete backup: ${response.statusText}`));
  }
};

/** 科学加速（0.6.206）：代理连通性测试（单独校验给定地址，不保存、不影响当前生效配置）。 */
export const testProxy = async (proxyURL: string): Promise<{ ok: boolean; latency_ms?: number; error?: string }> => {
  const response = await apiFetch(apiUrl('/api/settings/proxy-test'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ proxy_url: proxyURL }),
  });
  if (!response.ok) {
    throw new Error(await extractError(response, `Failed to test proxy: ${response.statusText}`));
  }
  return response.json();
};

/** 手动清理应用缓存中超过 N 天的文件（不碰已下载 FPK；不依赖自动清理开关）。
 *  force=true 时强制清空全部缓存文件（不设年龄阈值）。 */
export const cleanAppCache = async (days: number, force = false): Promise<{ ok: boolean; removed: number; freed_bytes: number }> => {
  const response = await apiFetch(apiUrl('/api/backups/clean'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ days, force }),
  });
  if (!response.ok) {
    throw new Error(await extractError(response, `Failed to clean app cache: ${response.statusText}`));
  }
  return response.json();
};

/** 下载备份文件（存到手机/电脑等外部设备）。 */
export const downloadBackup = async (name: string): Promise<Blob> => {
  const response = await apiFetch(apiUrl(`/api/backups/${encodeURIComponent(name)}/download`));
  if (!response.ok) {
    throw new Error(await extractError(response, `Failed to download backup: ${response.statusText}`));
  }
  return response.blob();
};

/** 用某个备份覆盖当前配置（随后应用自动重启）。 */
export const restoreBackup = async (name: string): Promise<{ ok: boolean; restarting: boolean }> => {
  const response = await apiFetch(apiUrl(`/api/backups/${encodeURIComponent(name)}/restore`), { method: 'POST' });
  if (!response.ok) {
    throw new Error(await extractError(response, `Failed to restore backup: ${response.statusText}`));
  }
  return response.json();
};

/** 可访问的 FPK 下载目录选项（/volN 卷根 + 卷下顶层共享目录）+ 当前生效目录。 */
export interface DownloadDirOption {
  path: string;
  label: string;
}

export const getDownloadDirOptions = async (): Promise<{ options: DownloadDirOption[]; current: string }> => {
  const response = await apiFetch(apiUrl('/api/settings/download-dirs'));
  if (!response.ok) {
    throw new Error(await extractError(response, `获取下载目录列表失败: ${response.statusText}`));
  }
  return response.json();
};

/** 目录浏览器单级列表：path="/" 列卷根；否则列该目录下可见子目录。 */
export const browseDownloadDirs = async (path: string): Promise<{
  path: string;
  parent: string;
  is_allowed: boolean;
  current: string;
  entries: { name: string; path: string }[];
}> => {
  const response = await apiFetch(apiUrl(`/api/settings/download-dirs/browse?path=${encodeURIComponent(path)}`));
  if (!response.ok) {
    throw new Error(await extractError(response, `读取目录失败: ${response.statusText}`));
  }
  return response.json();
};

export const fetchStatus = async (): Promise<StatusResponse> => {
  const response = await apiFetch(apiUrl('/api/status'));
  if (!response.ok) {
    throw new Error(`Failed to fetch status: ${response.statusText}`);
  }
  return response.json();
};

export const fetchStoreUpdate = async (): Promise<StoreUpdateInfo> => {
  const response = await apiFetch(apiUrl('/api/store-update'));
  if (!response.ok) {
    throw new Error(`Failed to fetch store update info: ${response.statusText}`);
  }
  return response.json();
};

export const triggerStoreUpdate = (onEvent: SSECallback): SSEHandle => {
  return streamSSE(apiUrl('/api/store-update'), onEvent);
};

// ---------- 应用收藏 ----------

export const fetchFavorites = async (): Promise<string[]> => {
  const response = await apiFetch(apiUrl('/api/favorites'));
  if (!response.ok) {
    throw new Error(`Failed to fetch favorites: ${response.statusText}`);
  }
  const data = await response.json();
  return Array.isArray(data.favorites) ? data.favorites : [];
};

export interface FavoriteToggleResult {
  favorited: boolean;
  favorites: string[];
}

/** 切换收藏（幂等：后端按 key 存在与否切换），返回切换后的完整集合。 */
export const toggleFavorite = async (key: string): Promise<FavoriteToggleResult> => {
  const response = await apiFetch(apiUrl('/api/favorites'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ key }),
  });
  if (!response.ok) {
    throw new Error(`Failed to toggle favorite: ${response.statusText}`);
  }
  return response.json();
};

export const ignoreUpdate = async (appname: string): Promise<void> => {
  const response = await apiFetch(apiUrl(`/api/apps/${appname}/ignore-update`), { method: 'PUT' });
  if (!response.ok) {
    throw new Error(`Failed to ignore update: ${response.statusText}`);
  }
};

export const unignoreUpdate = async (appname: string): Promise<void> => {
  const response = await apiFetch(apiUrl(`/api/apps/${appname}/ignore-update`), { method: 'DELETE' });
  if (!response.ok) {
    throw new Error(`Failed to unignore update: ${response.statusText}`);
  }
};

export interface DiagnosticReport {
  app: string;
  display_name: string;
  version?: string;
  arch: 'x86' | 'ARM';
  app_type?: string;
  failed_step: string;
  error_message: string;
  log_tail: string;
  log_truncated: boolean;
  store_version: string;
  platform: string;
  timestamp: string;
}

export interface DiagnosticResponse {
  report: DiagnosticReport;
  issue_url: string;
}

export async function fetchDiagnostic(app: string, step: string, errorMsg: string): Promise<DiagnosticResponse> {
  const params = new URLSearchParams({ step, error: errorMsg });
  const res = await apiFetch(apiUrl(`/api/apps/${encodeURIComponent(app)}/diagnostic?${params}`));
  if (!res.ok) throw new Error(`获取诊断信息失败: ${res.status}`);
  return res.json();
}

// ── 官方应用中心 OAuth 免登录通道（0.6.253） ─────────────────────────────
// 授权页（面板 /signin + PKCE）在 iframe 内打开；code 换 token 后目录走
// /ogh/ac/h 代理，不再触发面板登录限流。

export interface OfficialStatus {
  authorized: boolean;
  expired?: boolean;
  expires_at?: number;
  scopes?: string[];
  pending?: boolean;
  last_error?: string;
  /** 0.6.254：面板前端是否支持 PKCE 授权页（fnOS 1.2.0800+；旧版只渲染普通登录页） */
  ui_supported?: boolean;
  /** ui_supported 是否已探测完成（false = 检测中） */
  ui_known?: boolean;
}

export async function fetchOfficialStatus(): Promise<OfficialStatus> {
  const res = await apiFetch(apiUrl('/api/official/status'));
  if (!res.ok) throw new Error(`获取官方连接状态失败: ${res.status}`);
  return res.json();
}

/** 开始授权。base = 用户浏览器可达的面板地址（如 http://192.0.2.22:5666）。 */
export async function officialAuthorize(base?: string): Promise<{ url: string }> {
  const q = base ? `?base=${encodeURIComponent(base)}` : '';
  const res = await apiFetch(apiUrl(`/api/official/authorize${q}`));
  if (!res.ok) throw new Error(`发起授权失败: ${res.status}`);
  return res.json();
}

export async function officialCallback(code: string): Promise<void> {
  const res = await apiFetch(apiUrl('/api/official/callback'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ code }),
  });
  if (!res.ok) {
    let msg = `验证码提交失败: ${res.status}`;
    try { const j = await res.json(); if (j && j.error) msg = j.error; } catch { /* keep */ }
    throw new Error(msg);
  }
}

export async function officialCancel(): Promise<void> {
  await apiFetch(apiUrl('/api/official/cancel'), { method: 'POST' });
}

export async function officialLogout(): Promise<void> {
  await apiFetch(apiUrl('/api/official/logout'), { method: 'POST' });
}

/** 无头授权（0.6.255）：无需浏览器，临时面板账号登录取码换 token。
 *  username/password 仅本次请求使用，服务端不落盘/不缓存。 */
export async function officialAuthorizeHeadless(username: string, password: string): Promise<void> {
  const res = await apiFetch(apiUrl('/api/official/authorize-headless'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
  });
  if (!res.ok) {
    let msg = `无头授权失败: ${res.status}`;
    try { const j = await res.json(); if (j && j.error) msg = j.error; } catch { /* keep */ }
    throw new Error(msg);
  }
}

export interface OfficialStoreApp {
  appName: string;
  name: string;
  version: string;
  icon?: string;
  source?: string;
  sourceID?: string;
  status?: string;
  tags?: string[];
}

/** 授权后验证：拉官方全量目录（10 分钟缓存）。 */
export async function fetchOfficialApps(): Promise<{ total: number; list: OfficialStoreApp[] }> {
  const res = await apiFetch(apiUrl('/api/official/apps'));
  if (!res.ok) throw new Error(`拉取官方目录失败: ${res.status}`);
  return res.json();
}
