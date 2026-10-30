/**
 * 应用分类 key → 中文标签（0.6.235）。
 *
 * 后端 `mapCategory` 会把源里的 labels/categories/tags 映射成分类 key
 * （`media`/`ai`/`system`/`content` …），未收录的标签则**透传原文**后由
 * 归类器再判。详情页要展示「分类标签」，必须把 key 换回中文，否则会出现
 * `system` 这种英文 key。
 *
 * 与 App.tsx 的分类 pill 列表保持一致（那边是 {key,label,icon} 数组）。
 */
export const CATEGORY_LABELS: Record<string, string> = {
  ai: 'AI',
  media: '影音娱乐',
  automation: '媒体自动化',
  game: '游戏',
  photo: '摄影摄像',
  efficiency: '实用效率',
  devtools: '开发工具',
  lifestyle: '生活服务',
  backup: '备份同步',
  download: '下载',
  network: '网络工具',
  browser: '浏览器',
  driver: '驱动',
  other: '其他',
  system: '系统工具',
  content: '内容',
}

/** key → 中文；已是中文/未知值则原样返回。 */
export const categoryLabel = (key?: string): string => {
  const k = (key || '').trim()
  if (!k) return ''
  return CATEGORY_LABELS[k] || k
}
