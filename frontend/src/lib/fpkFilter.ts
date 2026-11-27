// 2026-11-26（用户令）：已下载 FPK 搜索的纯函数（便于回归测试）。
//
// 与应用源搜索同口径：输入即筛选、清空即还原；命中范围 = 应用名（appname）、
// 包文件名、显示名、版本、**应用源名**（source，由后端按 appname 反查目录）。
export interface FpkSearchable {
  name: string;
  appname?: string;
  display_name?: string;
  version?: string;
  source?: string;
}

export function filterFpkDownloads<T extends FpkSearchable>(files: T[], query: string): T[] {
  const q = query.trim().toLowerCase();
  if (!q) return files;
  return files.filter((f) => {
    const hay = [f.name, f.appname, f.display_name, f.version, f.source]
      .filter(Boolean)
      .join(' ')
      .toLowerCase();
    return hay.includes(q);
  });
}
