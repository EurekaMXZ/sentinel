import type { AgentEvent } from "./types";

export interface ModelAttemptItem {
  kind: "model";
  key: string;
  attempt: number;
  startedAt?: string;
  completedAt?: string;
  status: "streaming" | "completed" | "failed";
  text: string;
  argumentsText: string;
  itemId?: string;
  code?: string;
}

export interface ToolItem {
  kind: "tool";
  key: string;
  name: string;
  startedAt: string;
  completedAt?: string;
  status: "running" | "completed" | "failed";
}

export type StepItem = ModelAttemptItem | ToolItem;

export interface StepEntry {
  kind: "step";
  key: string;
  stepId: number;
  items: StepItem[];
  startedAt?: string;
  completed?: AgentEvent;
}

export interface MarkerEntry {
  kind: "status" | "compact" | "terminal";
  key: string;
  event: AgentEvent;
}

export type TimelineEntry = StepEntry | MarkerEntry;

export type EventFilter = "all" | "important" | "model" | "tools" | "status";

export function asRecord(value: unknown): Record<string, unknown> | undefined {
  if (value && typeof value === "object" && !Array.isArray(value)) {
    return value as Record<string, unknown>;
  }
  return undefined;
}

export function eventDataString(event: AgentEvent, key: string): string | undefined {
  const value = asRecord(event.data)?.[key];
  return typeof value === "string" ? value : undefined;
}

export function eventDataNumber(event: AgentEvent, key: string): number | undefined {
  const value = asRecord(event.data)?.[key];
  return typeof value === "number" ? value : undefined;
}

function findAttempt(step: StepEntry, attempt: number): ModelAttemptItem | undefined {
  for (let index = step.items.length - 1; index >= 0; index -= 1) {
    const item = step.items[index];
    if (item.kind === "model" && item.attempt === attempt) return item;
  }
  return undefined;
}

function lastModel(step: StepEntry): ModelAttemptItem | undefined {
  for (let index = step.items.length - 1; index >= 0; index -= 1) {
    const item = step.items[index];
    if (item.kind === "model") return item;
  }
  return undefined;
}

function lastRunningTool(step: StepEntry, name: string): ToolItem | undefined {
  for (let index = step.items.length - 1; index >= 0; index -= 1) {
    const item = step.items[index];
    if (item.kind === "tool" && item.name === name && item.status === "running") {
      return item;
    }
  }
  return undefined;
}

function newAttempt(event: AgentEvent, attempt: number): ModelAttemptItem {
  return {
    kind: "model",
    key: `model-${event.id}`,
    attempt,
    startedAt: event.created_at,
    status: "streaming",
    text: "",
    argumentsText: "",
  };
}

/**
 * Group the flat SSE event log into readable step blocks while preserving the
 * original event order. The raw view still renders every event.
 */
export function buildTimeline(events: AgentEvent[]): TimelineEntry[] {
  const entries: TimelineEntry[] = [];
  const steps = new Map<number, StepEntry>();

  const ensureStep = (stepId: number): StepEntry => {
    let step = steps.get(stepId);
    if (!step) {
      step = { kind: "step", key: `step-${stepId}`, stepId, items: [] };
      steps.set(stepId, step);
      entries.push(step);
    }
    return step;
  };

  for (const event of events) {
    const stepId = event.step_id ?? 0;
    switch (event.type) {
      case "agent.status":
      case "context.compacted": {
        entries.push({
          kind: event.type === "agent.status" ? "status" : "compact",
          key: `event-${event.id}`,
          event,
        });
        break;
      }
      case "model.started": {
        const step = ensureStep(stepId);
        const attempt = event.attempt ?? 0;
        step.startedAt ??= event.created_at;
        step.items.push(newAttempt(event, attempt));
        break;
      }
      case "model.delta": {
        const step = ensureStep(stepId);
        const attempt = event.attempt ?? 0;
        let item = findAttempt(step, attempt);
        if (!item) {
          item = newAttempt(event, attempt);
          step.items.push(item);
        }
        step.startedAt ??= event.created_at;
        const data = asRecord(event.data);
        const delta = typeof data?.text === "string" ? data.text : "";
        if (data?.kind === "arguments") {
          item.argumentsText += delta;
        } else {
          item.text += delta;
        }
        if (typeof data?.item_id === "string") item.itemId = data.item_id;
        break;
      }
      case "model.completed": {
        const step = ensureStep(stepId);
        const item = findAttempt(step, event.attempt ?? 0) ?? lastModel(step);
        if (item) {
          item.status = "completed";
          item.completedAt = event.created_at;
        }
        break;
      }
      case "model.failed": {
        const step = ensureStep(stepId);
        const item = findAttempt(step, event.attempt ?? 0) ?? lastModel(step);
        if (item) {
          item.status = "failed";
          item.completedAt = event.created_at;
          item.code =
            eventDataString(event, "code") ?? eventDataString(event, "status") ?? "model_failed";
        }
        break;
      }
      case "tool.started": {
        const step = ensureStep(stepId);
        const name = eventDataString(event, "tool") ?? "tool";
        step.startedAt ??= event.created_at;
        step.items.push({
          kind: "tool",
          key: `tool-${event.id}`,
          name,
          startedAt: event.created_at,
          status: "running",
        });
        break;
      }
      case "tool.completed": {
        const step = ensureStep(stepId);
        const name = eventDataString(event, "tool") ?? "tool";
        let item = lastRunningTool(step, name);
        if (!item) {
          item = {
            kind: "tool",
            key: `tool-${event.id}`,
            name,
            startedAt: event.created_at,
            status: "running",
          };
          step.items.push(item);
        }
        item.completedAt = event.created_at;
        item.status = eventDataString(event, "status") === "failed" ? "failed" : "completed";
        break;
      }
      case "step.completed": {
        const step = ensureStep(stepId);
        step.completed = event;
        for (const item of step.items) {
          if (item.kind === "model" && item.status === "streaming") {
            item.status = "completed";
            item.completedAt = event.created_at;
          }
        }
        break;
      }
      case "agent.completed":
      case "agent.failed":
      case "agent.cancelled": {
        entries.push({ kind: "terminal", key: `event-${event.id}`, event });
        break;
      }
      default: {
        entries.push({ kind: "status", key: `event-${event.id}`, event });
      }
    }
  }

  return entries;
}

const MODEL_TYPES = new Set([
  "model.started",
  "model.delta",
  "model.completed",
  "model.failed",
]);

const STATUS_TYPES = new Set([
  "agent.status",
  "context.compacted",
  "agent.completed",
  "agent.failed",
  "agent.cancelled",
  "step.completed",
]);

export function filterEvents(
  events: AgentEvent[],
  filter: EventFilter,
  query: string,
): AgentEvent[] {
  const needle = query.trim().toLowerCase();
  return events.filter((event) => {
    if (filter === "important" && event.type === "model.delta") return false;
    if (filter === "model" && !MODEL_TYPES.has(event.type) && event.type !== "step.completed") {
      return false;
    }
    if (filter === "tools" && !event.type.startsWith("tool.")) return false;
    if (filter === "status" && !STATUS_TYPES.has(event.type)) return false;
    if (needle) {
      const haystack = JSON.stringify(event).toLowerCase();
      if (!haystack.includes(needle)) return false;
    }
    return true;
  });
}

export function eventSummary(event: AgentEvent): string {
  switch (event.type) {
    case "agent.status":
      return eventDataString(event, "status") ?? "";
    case "context.compacted": {
      const version = eventDataNumber(event, "context_version");
      return version === undefined ? "" : `context v${version}`;
    }
    case "model.started":
      return event.attempt ? `attempt ${event.attempt}` : "";
    case "model.delta": {
      const kind = eventDataString(event, "kind") ?? "text";
      const text = eventDataString(event, "text") ?? "";
      return `${kind}: ${text.length > 80 ? `${text.slice(0, 80)}…` : text}`;
    }
    case "model.completed":
      return eventDataString(event, "status") ?? "completed";
    case "model.failed":
      return eventDataString(event, "code") ?? "failed";
    case "tool.started":
    case "tool.completed":
      return [eventDataString(event, "tool"), eventDataString(event, "status")]
        .filter(Boolean)
        .join(" · ");
    case "step.completed": {
      const version = eventDataNumber(event, "context_version");
      const sequence = eventDataNumber(event, "event_sequence");
      return [version === undefined ? "" : `context v${version}`, sequence === undefined ? "" : `event #${sequence}`]
        .filter(Boolean)
        .join(" · ");
    }
    case "agent.completed":
      return eventDataString(event, "result_ref") ?? "completed";
    case "agent.failed":
      return [eventDataString(event, "status"), eventDataString(event, "code")].filter(Boolean).join(" · ");
    case "agent.cancelled":
      return [eventDataString(event, "status"), eventDataString(event, "code")].filter(Boolean).join(" · ");
    default:
      return "";
  }
}
