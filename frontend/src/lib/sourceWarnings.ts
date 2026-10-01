/**
 * 应用源展示层的告警规则（纯函数，便于单测）。
 *
 * 背景：0.6.216 P1① 给「明文 http 源」加了 ShieldAlert 感叹号；
 * 但飞牛官方应用源（fnos-official）的 url 是本机面板地址（http + LAN），
 * 属平台可信来源，对它标「未加密」只会误导用户 → 豁免。
 */

/** 官方应用源的固定 id（与后端 OfficialSourceID 一致）。 */
export const OFFICIAL_SOURCE_ID = 'fnos-official'

/** 是否为明文 http 地址（无 scheme 的源按 https 处理，不算明文）。 */
export const isPlainHttp = (url?: string): boolean => !!url && /^http:\/\//i.test(url)

/**
 * 是否应展示「明文 http 源」警示。
 *
 * - 官方应用源：永不展示（平台本机面板地址，可信）
 * - 其余 http:// 源（社区源 / 自建源）：展示，提示核对 sha256
 * - https:// 或无 scheme：不展示
 */
export const shouldWarnPlainHttp = (source: { id?: string; url?: string }): boolean =>
  source.id !== OFFICIAL_SOURCE_ID && isPlainHttp(source.url)
