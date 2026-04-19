import type { AgentStatus } from "./types";

export type Tone = "default" | "accent" | "success" | "warning" | "danger";

const STATUS_LABELS: Record<string, string> = {
  pending: "等待调度",
  running: "运行中",
  waiting: "等待中",
  cancelling: "取消中",
  queued: "排队中",
  archiving: "归档中",
  publishing: "发布中",
  succeeded: "已完成",
  completed: "已完成",
  failed: "失败",
  cancelled: "已取消",
};

export function statusLabel(status: string | undefined): string {
  if (!status) return "未知";
  return STATUS_LABELS[status] ?? status;
}

export function statusTone(status: string | undefined): Tone {
  switch (status) {
    case "running":
    case "queued":
    case "archiving":
    case "publishing":
    case "cancelling":
      return "accent";
    case "completed":
    case "succeeded":
      return "success";
    case "waiting":
      return "warning";
    case "failed":
      return "danger";
    default:
      return "default";
  }
}

export function isAgentTerminal(status: AgentStatus | string | undefined): boolean {
  return status === "completed" || status === "failed" || status === "cancelled";
}

export function formatDateTime(value?: string | null): string {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  }).format(date);
}

export function formatClock(value?: string | null): string {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat("zh-CN", {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).format(date);
}

export function formatRelative(value?: string | null): string {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  const seconds = Math.round((date.getTime() - Date.now()) / 1000);
  if (Math.abs(seconds) < 45) return "刚刚";
  const formatter = new Intl.RelativeTimeFormat("zh-CN", { numeric: "auto" });
  const units: Array<[Intl.RelativeTimeFormatUnit, number]> = [
    ["year", 365 * 24 * 3600],
    ["month", 30 * 24 * 3600],
    ["day", 24 * 3600],
    ["hour", 3600],
    ["minute", 60],
    ["second", 1],
  ];
  for (const [unit, size] of units) {
    if (Math.abs(seconds) >= size) {
      return formatter.format(Math.round(seconds / size), unit);
    }
  }
  return formatter.format(seconds, "second");
}

export function formatDuration(start?: string | null, end?: string | null): string {
  if (!start) return "—";
  const from = new Date(start).getTime();
  if (Number.isNaN(from)) return "—";
  const to = end ? new Date(end).getTime() : Date.now();
  if (Number.isNaN(to) || to < from) return "—";
  const total = Math.round((to - from) / 1000);
  if (total < 60) return `${total} 秒`;
  const minutes = Math.floor(total / 60);
  const seconds = total % 60;
  if (minutes < 60) return `${minutes} 分 ${seconds} 秒`;
  const hours = Math.floor(minutes / 60);
  return `${hours} 小时 ${minutes % 60} 分`;
}

const EVENT_LABELS: Record<string, string> = {
  "agent.status": "Agent 状态",
  "step.completed": "步骤完成",
  "model.started": "模型开始",
  "model.text": "模型文本",
  "model.arguments": "工具参数",
  "model.delta": "模型增量（旧）",
  "model.completed": "模型完成",
  "model.failed": "模型失败",
  "tool.started": "工具开始",
  "tool.completed": "工具完成",
  "context.compacted": "Context 压缩",
  "agent.completed": "Agent 完成",
  "agent.failed": "Agent 失败",
  "agent.cancelled": "Agent 取消",
};

export function repositoryLabel(url?: string | null): string {
  if (!url) return "";
  try {
    const parsed = new URL(url);
    const parts = parsed.pathname.replace(/^\/+|\/+$/g, "").split("/");
    if (parts.length >= 2) return `${parts[0]}/${parts[1].replace(/\.git$/, "")}`;
    return parsed.pathname;
  } catch {
    return url;
  }
}

export function eventTypeLabel(type: string): string {
  return EVENT_LABELS[type] ?? type;
}

export function eventTone(type: string): Tone {
  if (type === "agent.failed" || type === "model.failed") return "danger";
  if (type === "agent.completed") return "success";
  if (type === "agent.cancelled") return "warning";
  if (type === "context.compacted") return "warning";
  if (type.startsWith("tool.")) return "accent";
  if (type.startsWith("model.")) return "accent";
  return "default";
}
