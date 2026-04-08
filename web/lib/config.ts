export const TENANT_ID =
  process.env.NEXT_PUBLIC_TENANT_ID?.trim() ||
  "0194e229-8576-7f42-8f08-3496913847cf";

export const DEFAULT_TASK =
  "请只读分析这个仓库：概括项目用途与整体架构，说明核心模块及其职责和主要数据流，" +
  "指出明显的缺陷或风险，并为每个结论给出源码文件与行号证据。不要修改任何文件。";

const GITHUB_NAME = /^[A-Za-z0-9_.-]+$/;

/**
 * Mirror the backend's NormalizeGitHubURL rules so the form can reject bad
 * input before creating a repository record.
 */
export function normalizeGitHubUrl(raw: string): string {
  const value = raw.trim();
  if (!value) {
    throw new Error("请输入 GitHub 仓库地址");
  }
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    throw new Error("地址格式不正确，需要 https://github.com/owner/repository");
  }
  if (
    url.protocol !== "https:" ||
    url.hostname.toLowerCase() !== "github.com" ||
    url.username ||
    url.password ||
    url.search ||
    url.hash
  ) {
    throw new Error("只支持 https://github.com/owner/repository 形式的公开仓库地址");
  }
  const parts = url.pathname.replace(/\/+$/, "").split("/");
  if (parts.length !== 3 || parts[0] !== "") {
    throw new Error("地址必须指向一个 GitHub 仓库根目录");
  }
  const owner = parts[1];
  const name = parts[2].replace(/\.git$/, "");
  for (const part of [owner, name]) {
    if (!GITHUB_NAME.test(part) || part === "." || part === ".." || part.length > 100) {
      throw new Error("GitHub owner 或仓库名不合法");
    }
  }
  return `https://github.com/${owner}/${name}.git`;
}

export function shortId(id: string, length = 8): string {
  if (id.length <= length) return id;
  return id.slice(0, length);
}

export function isTerminalStatus(status: string | undefined): boolean {
  return status === "completed" || status === "failed" || status === "cancelled";
}
