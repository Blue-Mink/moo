/**
 * 0.6.325 C1（issue#5 遗留改进项）：卷显示名优先级。
 *
 * 有用户友好名（name）→「友好名 · /volN」；无 →「存储空间 N · /volN」。
 *
 * 2026-10-14 主 NAS 实机侦察结论（详见 FIX_325C_IMPL.md）：本版本 fnOS
 * （1.2.1）找不到可靠的卷友好名来源——面板存储卷渲染恒为「存储空间 {id}」
 * 且无重命名入口（locale 无任何存储卷 rename 项）；PG open_gateway 无卷表、
 * trim.mount 的 comment 列恒空、LVM/btrfs label 只有 UUID/空。故后端
 * VolumeOption.Name 当前恒空，本函数实际恒走第二分支（与 C1 前行为一致）；
 * 显示优先级就位，来源一旦可用前端零改动。
 */
export interface VolumeLabelInput {
  index: number;
  path?: string;
  name?: string;
}

/** 卷选项显示文案（设置页安装位置下拉等卷名面共用）。 */
export function volumeOptionLabel(v: VolumeLabelInput): string {
  const pathPart = v.path ? ` · ${v.path}` : '';
  const name = (v.name ?? '').trim();
  if (name) return `${name}${pathPart}`;
  return `存储空间 ${v.index}${pathPart}`;
}
