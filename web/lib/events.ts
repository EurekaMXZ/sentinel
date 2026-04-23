import type { AgentEvent } from "./types";

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
