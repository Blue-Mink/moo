/**
 * 应用元数据的展示口径（0.6.241）。
 */

/**
 * 运行方式 / 安装位置的展示：
 *  - `package`（用户态安装）→ **用户空间**（对用户更直观）
 *  - `root` → 原样 root
 *  - `system` / `系统空间` → 系统空间
 *  - 其它未知值原样透传（不猜）
 */
/**
 * 0.6.243：install_type 这一个字段在不同源里语义混杂（面板给的是**安装位置**
 * 存储空间/系统空间；FPK/manifest 给的是**运行身份** root/package）。
 * 而且实测有源往里塞分类名（影视/工具/音乐…）。
 *
 * 处理：识别成「运行身份」→ 用标签「运行方式」；识别成「安装位置」→ 用标签
 * 「安装位置」；**认不出来的值整行不显示**（避免把分类名当运行方式展示）。
 */
export const installTypeRow = (v?: string): { label: string; value: string } | null => {
  const raw = (v || '').trim()
  const k = raw.toLowerCase()
  if (!k) return null
  if (['root', 'sudo', 'administrator'].includes(k)) return { label: '运行方式', value: k === 'root' ? 'root' : raw }
  if (['package', 'user', 'user-space', 'userspace', '用户空间'].includes(k)) return { label: '运行方式', value: '用户空间' }
  if (['system', 'system-space', '系统空间', '系統空間'].includes(k)) return { label: '安装位置', value: '系统空间' }
  if (['storage', 'volume', 'storage-space', '存储空间'].includes(k)) return { label: '安装位置', value: '存储空间' }
  return null
}

export const installTypeLabel = (v?: string): string => {
  const k = (v || '').trim().toLowerCase()
  switch (k) {
    case 'package':
    case 'user':
    case 'user-space':
    case 'userspace':
    case '用户空间':
      return '用户空间'
    case 'root':
      return 'root'
    case 'system':
    case 'system-space':
    case '系统空间':
      return '系统空间'
    default:
      return (v || '').trim()
  }
}
