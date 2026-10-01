/**
 * fnOS Web UI 壳窗口 ⇄ iframe 应用的 postmate 消息桥（子端实现）。
 *
 * 协议逆向自测试机 /usr/trim/www/assets/index-*.js 的父端实现（fnOS
 * 1.2.0505）：壳窗口为每个 iframe 应用建立 penpal 连接，握手流程：
 *
 *   子 → 父  {penpal:"syn",    id, appName, methodNames}
 *   父 → 子  {penpal:"synAck", id, appName, config, methodNames}   // 父端方法清单
 *   子 → 父  {penpal:"ack",    methodNames}                        // 子端方法清单（我们无）
 *   子 → 父  {penpal:"call",   id, methodName, args}
 *   父 → 子  {penpal:"reply",  id, resolution, returnValue}
 *
 * 父端只校验消息来源 origin（= iframe src 的 origin），不校验 call id，
 * 因此子端可以任意生成 id。父端暴露的方法（实测 methodNames）：
 *   bus / genReqId / query / openApp / openFile / openFileManager /
 *   openFileManagerApp / openCustomApp / openAppSetting / setTitle /
 *   pickFile / pickUserFile / pickSharedFile / authorizeUserFile /
 *   authorizeSharedFile / showFileDetails / close / refreshToken /
 *   setExitPageTips / getPlatformConfig
 *
 * 本模块只封装我们需要的部分：openCustomApp（在壳内打开任意应用视图，
 * 等价于应用中心"应用设置"按钮的 CQ(Q.Setting, aQ.Application, {params})）。
 * 独立打开（非 iframe 内嵌）或握手失败时桥不可用，调用方自行降级。
 */

const SYN = 'syn';
const SYN_ACK = 'synAck';
const ACK = 'ack';
const CALL = 'call';
const REPLY = 'reply';
const REJECTED = 'rejected';

interface FnsMsg {
  penpal: string;
  id?: string;
  appName?: string;
  methodNames?: string[];
  config?: unknown;
  methodName?: string;
  args?: unknown[];
  resolution?: string;
  returnValue?: unknown;
  returnValueIsError?: boolean;
}

let parentMethods: string[] | null = null;
let handshakePromise: Promise<string[] | null> | null = null;

const genId = () =>
  `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 10)}`;

const isEmbedded = () => {
  try {
    return window.parent !== window;
  } catch {
    return false;
  }
};

/**
 * 握手：发 syn，等父端 synAck（含父端方法清单）。
 * 父端监听器在 iframe 挂载时建立，页面 JS 可能先于父端就绪，
 * 因此有限重试；总共约 6 秒，静默失败（独立打开属正常情况）。
 *
 * 注意：握手【失败不缓存】——每次调用重新尝试，保证用户点击按钮时
 * 若父端此刻已就绪（如 tab 被挂起恢复、壳延迟注册监听器）仍可成功。
 */
export function connectFnOSBridge(): Promise<string[] | null> {
  if (!isEmbedded()) return Promise.resolve(null);
  if (parentMethods) return Promise.resolve(parentMethods);
  if (handshakePromise) return handshakePromise;

  const promise = new Promise<string[] | null>((resolve) => {
    let settled = false;
    let attempts = 0;
    const maxAttempts = 12; // ~6s
    let timer: ReturnType<typeof setTimeout>;

    const onMessage = (e: MessageEvent) => {
      const data = e.data as FnsMsg | null;
      if (!data || typeof data !== 'object' || data.penpal !== SYN_ACK) return;
      // 父端只按 origin 过滤，不回显子端 id；收到首个 synAck 即视为本 iframe 的握手应答
      parentMethods = data.methodNames || [];
      console.info(`[fnos-bridge] synAck 收到，父端方法 ${parentMethods.length} 个`, parentMethods);
      settled = true;
      window.removeEventListener('message', onMessage);
      clearTimeout(timer);
      // 回 ack 完成父端建链（父端不校验 id，只取 methodNames）
      try {
        window.parent.postMessage({ penpal: ACK, methodNames: [] } satisfies FnsMsg, '*');
        console.info('[fnos-bridge] ack 已发送，握手完成');
      } catch {
        /* 忽略 */
      }
      resolve(parentMethods);
    };

    const sendSyn = () => {
      attempts += 1;
      if (attempts === 1) console.info('[fnos-bridge] 开始握手（syn）');
      try {
        window.parent.postMessage(
          { penpal: SYN, id: genId(), appName: 'fnos-apps-store', methodNames: [] } satisfies FnsMsg,
          '*',
        );
      } catch {
        /* 忽略 */
      }
      if (settled) return;
      if (attempts >= maxAttempts) {
        window.removeEventListener('message', onMessage);
        clearTimeout(timer);
        console.warn('[fnos-bridge] 握手超时（父端未应答 synAck）');
        resolve(null);
        return;
      }
      timer = setTimeout(sendSyn, 500);
    };

    window.addEventListener('message', onMessage);
    sendSyn();
  });

  // 失败不缓存：清理 promise 让下次调用可以重新握手
  handshakePromise = promise;
  void promise.then((m) => {
    if (!m) handshakePromise = null;
  });
  return promise;
}

/** 桥是否已握手成功（父端方法清单非空）。 */
export function fnOSBridgeReady(): boolean {
  return (parentMethods?.length ?? 0) > 0;
}

/** 父端是否暴露指定方法。 */
export function fnOSHasMethod(name: string): boolean {
  return (parentMethods ?? []).includes(name);
}

/**
 * 调用父端方法。未握手成功时 reject('bridge-unavailable')。
 * 父端 reply 的 returnValueIsError 时 returnValue 为错误信息对象。
 */
export function callFnOSMethod<T = unknown>(
  methodName: string,
  ...args: unknown[]
): Promise<T> {
  if (!parentMethods) {
    return Promise.reject(new Error('bridge-unavailable'));
  }
  const id = genId();
  console.info(`[fnos-bridge] call ${methodName}()`, args);
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(() => {
      window.removeEventListener('message', onMessage);
      console.warn(`[fnos-bridge] ${methodName} 调用超时`);
      reject(new Error('bridge-timeout'));
    }, 30_000);

    const onMessage = (e: MessageEvent) => {
      const data = e.data as FnsMsg | null;
      if (!data || typeof data !== 'object' || data.penpal !== REPLY || data.id !== id) return;
      window.removeEventListener('message', onMessage);
      clearTimeout(timer);
      console.info(`[fnos-bridge] ${methodName} reply`, data.resolution, data.returnValue);
      if (data.resolution === REJECTED || data.returnValueIsError) {
        const msg = data.returnValue as { message?: string } | string | undefined;
        reject(new Error(typeof msg === 'string' ? msg : msg?.message || '父端调用失败'));
        return;
      }
      resolve(data.returnValue as T);
    };

    window.addEventListener('message', onMessage);
    try {
      window.parent.postMessage(
        { penpal: CALL, id, methodName, args } satisfies FnsMsg,
        '*',
      );
    } catch {
      window.removeEventListener('message', onMessage);
      clearTimeout(timer);
      reject(new Error('bridge-unavailable'));
    }
  });
}

/** 当前页面是否内嵌在 fnOS Web UI 壳窗口中（iframe）。 */
export function isEmbeddedInFnOS(): boolean {
  return isEmbedded();
}

/**
 * 在 fnOS Web UI 壳内打开某个应用（与原生应用中心"打开"按钮同机制：
 * 壳内任务标签页，而不是新浏览器标签）。
 *
 * 【关键】必须走父端 `openCustomApp(serviceName, {})`（→ 壳内 CQ，
 * 与原生"打开" He(serviceName) === CQ(serviceName) 完全一致）：
 *
 *   - 父端 `openApp(link)`（→ 壳内 SQ）用 `new URL('https://'+link)` 解析，
 *     hostname 会被【小写化】；而壳的解析器 x0 用 `appName === key`
 *     严格区分大小写。serviceName 如 "Gitea.Application" 被 SQ 小写成
 *     "gitea.application" 后查不到应用 → x0 静默 return，且 SQ 不抛错、
 *     父端 reply 仍是 resolved —— 于是按钮"点击无反应"（这就是 1.14.4
 *     打开按钮失效的根因，所有含大写字符的 serviceName 全部中招）。
 *   - CQ 用 `encodeURIComponent` 逐段编码、保留大小写，emit 出的 key 与
 *     应用注册表 appName 完全一致，因此能正确命中并在壳内开任务标签。
 *   - 第 2 参必须传 {}（不能省略）：CQ 对 undefined 尾部参会
 *     `encodeURIComponent(undefined)` 出 "…/undefined" 污染子视图；
 *     {} 会被 CQ 识别为参数对象并 pop 掉，得到干净的单段 key。
 *
 * 返回 false 表示桥不可用（独立打开 :38011 等场景），调用方降级处理。
 */
export async function openAppInShell(serviceName: string): Promise<boolean> {
  const methods = await connectFnOSBridge();
  if (!methods || !methods.includes('openCustomApp')) {
    console.info('[fnos-bridge] openAppInShell: 桥不可用或无 openCustomApp', {
      embedded: isEmbedded(),
      methods,
    });
    return false;
  }
  try {
    console.info(`[fnos-bridge] 调用 openCustomApp(${serviceName}, {})`);
    await callFnOSMethod('openCustomApp', serviceName, {});
    console.info('[fnos-bridge] openCustomApp 成功');
    return true;
  } catch (e) {
    console.warn('[fnos-bridge] openCustomApp 失败，调用方降级', e);
    return false;
  }
}

/**
 * 在 fnOS Web UI 壳内打开「设置 → 应用」页并自动弹出指定应用的
 * 「应用设置」面板 —— 与原生应用中心详情页第 4 个按钮
 * （CQ(Q.Setting, aQ.Application, {params:{appName}})）完全等价。
 *
 * 优先调用父端 `openAppSetting()`（无参数，父端自动使用当前 iframe
 * 的应用名，避免参数序列化差异）；若父端未暴露该方法或调用抛错，
 * 退回 `openCustomApp('trim.setting','application-settings',{params:{appName}})`。
 *
 * 返回 false 表示桥不可用（独立打开 :38011 等场景），调用方降级处理。
 */
export async function openAppSettings(appName: string): Promise<boolean> {
  const methods = await connectFnOSBridge();
  if (!methods || methods.length === 0) {
    console.info('[fnos-bridge] openAppSettings: 桥不可用', { embedded: isEmbedded() });
    return false;
  }
  if (methods.includes('openAppSetting')) {
    try {
      console.info('[fnos-bridge] 调用 openAppSetting()');
      await callFnOSMethod('openAppSetting');
      console.info('[fnos-bridge] openAppSetting 成功');
      return true;
    } catch (e) {
      console.warn('[fnos-bridge] openAppSetting 失败，尝试 openCustomApp', e);
    }
  }
  if (methods.includes('openCustomApp')) {
    try {
      console.info(`[fnos-bridge] 调用 openCustomApp(trim.setting, application-settings, {appName:${appName}})`);
      await callFnOSMethod('openCustomApp', 'trim.setting', 'application-settings', {
        params: { appName },
      });
      console.info('[fnos-bridge] openCustomApp 成功');
      return true;
    } catch (e) {
      console.warn('[fnos-bridge] openCustomApp 失败', e);
    }
  }
  console.warn('[fnos-bridge] 父端未暴露 openAppSetting/openCustomApp', methods);
  return false;
}
