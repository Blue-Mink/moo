/**
 * 0.6.342：已装应用的运行状态展示口径（列表卡面 / 行列表 / 网格共用）。
 *
 * 背景：daemon 的 status 此前只在详情页体现——列表里停用的应用仍显示
 * 「已安装」并给「打开」按钮（打开会先隐式启动），用户看不出它没在运行。
 * 本模块把「已安装 + 已停止」统一口径为「已停止」，并给出「启动」动作判定。
 *
 * status 取值（daemon /rpc/v1/app/installed）：
 *   running / stopped / starting / stopping /
 *   nostart（系统组件，无独立启停，不显示停用标记）/
 *   start（历史值：已启动但无服务态，保守按普通已安装处理）
 */

export type AppRunState = 'running' | 'stopped' | 'starting' | 'stopping' | 'nostart' | 'start' | '';

interface RunStateInput {
  installed?: boolean;
  status?: string;
  start_stop?: boolean;
}

/** 归一运行状态：未安装恒为 ''（列表里未装卡不显示运行态）。 */
export const appRunState = (app?: RunStateInput | null): AppRunState => {
  if (!app || !app.installed) return '';
  return (app.status || '') as AppRunState;
};

/**
 * 列表徽章文案（0.6.348 全端统一口径）。
 *
 * 此前 WebAppDetailCard（桌面网页卡）用自己的一份字典，running 显示「运行中」，
 * 而 AppRowList / AppCard / 极简极光网格走本函数，running 回落「已安装」——
 * 同一个应用在桌面显示「运行中」、在手机上显示「已安装」，用户以为状态丢了。
 * 现在统一：运行中/已停止/启动中/停用中/未启动 全部如实显示。
 */
export const installedStateLabel = (app?: RunStateInput | null): string => {
  if (!app || !app.installed) return '未安装';
  switch (appRunState(app)) {
    case 'running': return '运行中';
    case 'stopped': return '已停止';
    case 'starting': return '启动中';
    case 'stopping': return '停用中';
    case 'nostart': return '未启动';
    default: return '已安装';
  }
};

/** 停用/过渡态是否需要用警示色（琥珀）而非「已安装」的绿色。 */
export const isStoppedState = (app?: RunStateInput | null): boolean => {
  const s = appRunState(app);
  return s === 'stopped' || s === 'starting' || s === 'stopping';
};

/**
 * 列表里是否给出「启动」按钮：已安装 + daemon 声明支持启停 + 当前已停止。
 * starting/stopping 过渡态不给按钮（避免重复下发启停指令）。
 */
export const canStartInList = (app?: RunStateInput | null): boolean =>
  !!app && !!app.installed && (app.start_stop ?? true) && appRunState(app) === 'stopped';
