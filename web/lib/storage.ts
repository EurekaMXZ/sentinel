import type { RecentAnalysis } from "./types";

const RECENT_KEY = "sentinel.recent-analyses.v1";
const MAX_RECENT = 12;

function readJSON<T>(key: string): T | null {
  if (typeof window === "undefined") return null;
  try {
    const raw = window.localStorage.getItem(key);
    if (!raw) return null;
    return JSON.parse(raw) as T;
  } catch {
    return null;
  }
}

function writeJSON(key: string, value: unknown): void {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(key, JSON.stringify(value));
  } catch {
    // Storage can be unavailable in private browsing or when the quota is full.
  }
}

export function loadRecent(): RecentAnalysis[] {
  const value = readJSON<RecentAnalysis[]>(RECENT_KEY);
  if (!Array.isArray(value)) return [];
  return value.filter(
    (entry) =>
      entry &&
      typeof entry.agentId === "string" &&
      typeof entry.url === "string" &&
      typeof entry.task === "string" &&
      typeof entry.createdAt === "string",
  );
}

export function findRecent(agentId: string): RecentAnalysis | undefined {
  return loadRecent().find((entry) => entry.agentId === agentId);
}

export function addRecent(entry: RecentAnalysis): void {
  const existing = loadRecent().filter((item) => item.agentId !== entry.agentId);
  writeJSON(RECENT_KEY, [entry, ...existing].slice(0, MAX_RECENT));
}
