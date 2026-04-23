import { asRecord, eventDataString } from "./events";
import type { AgentEvent } from "./types";

export type CommandStatus = "running" | "ok" | "failed";
export type TerminalStatus = "completed" | "failed" | "cancelled";

export type ConsoleLine =
  | { kind: "step"; key: string; stepId: number; time: string }
  | {
      kind: "command";
      key: string;
      callId: string;
      tool: string;
      arguments: string;
      status: CommandStatus;
      time: string;
    }
  | { kind: "output"; key: string; text: string; time: string }
  | { kind: "error"; key: string; text: string; time: string }
  | { kind: "terminal"; key: string; status: TerminalStatus; code?: string; time: string };

export interface ConsoleModel {
  lines: ConsoleLine[];
  report?: string;
}

function terminalStatus(event: AgentEvent): TerminalStatus | undefined {
  switch (event.type) {
    case "agent.completed":
      return "completed";
    case "agent.failed":
      return "failed";
    case "agent.cancelled":
      return "cancelled";
    default:
      return undefined;
  }
}

function singleLine(text: string): string {
  return text.replace(/\s+/g, " ").trim();
}

// Keep the raw JSON readable on one console line without changing the value.
function compactArguments(raw: string): string {
  const trimmed = raw.trim();
  if (!trimmed) return "";
  try {
    const parsed: unknown = JSON.parse(trimmed);
    return JSON.stringify(parsed) ?? trimmed;
  } catch {
    return trimmed.replace(/\s*\r?\n\s*/g, " ");
  }
}

export function buildConsole(events: AgentEvent[]): ConsoleModel {
  const terminal = [...events].reverse().find((event) => terminalStatus(event));
  const finalStatus = terminal ? terminalStatus(terminal) : undefined;
  const finalStepId = terminal?.step_id;
  const reportParts: string[] = [];
  const lines: ConsoleLine[] = [];
  const steps = new Set<number>();
  const commands = new Map<string, Extract<ConsoleLine, { kind: "command" }>>();
  const legacyText = new Map<string, string>();
  let sequence = 0;
  const key = () => `console-${sequence++}`;

  function addStep(stepId: number | undefined, time: string) {
    if (!stepId || steps.has(stepId)) return;
    steps.add(stepId);
    lines.push({ kind: "step", key: key(), stepId, time });
  }

  function addText(stepId: number | undefined, time: string, text: string) {
    const normalized = singleLine(text);
    if (!normalized) return;
    if (finalStatus === "completed" && stepId === finalStepId) {
      reportParts.push(text.trim());
      return;
    }
    lines.push({ kind: "output", key: key(), text: normalized, time });
  }

  for (const event of events) {
    const stepId = event.step_id;
    if (stepId) addStep(stepId, event.created_at);

    switch (event.type) {
      case "model.text":
        addText(stepId, event.created_at, eventDataString(event, "text") ?? "");
        break;
      case "model.delta": {
        const data = asRecord(event.data);
        if (data?.kind === "arguments") break;
        const text = typeof data?.text === "string" ? data.text : "";
        if (!text) break;
        const legacyKey = `${stepId ?? 0}:${event.attempt ?? 0}`;
        legacyText.set(legacyKey, (legacyText.get(legacyKey) ?? "") + text);
        break;
      }
      case "model.completed": {
        const legacyKey = `${stepId ?? 0}:${event.attempt ?? 0}`;
        const text = legacyText.get(legacyKey);
        if (text) {
          addText(stepId, event.created_at, text);
          legacyText.delete(legacyKey);
        }
        break;
      }
      case "tool.started": {
        const tool = eventDataString(event, "tool") ?? "tool";
        const callId = eventDataString(event, "call_id") ?? `${tool}-${event.id}`;
        const line: Extract<ConsoleLine, { kind: "command" }> = {
          kind: "command",
          key: key(),
          callId,
          tool,
          arguments: compactArguments(eventDataString(event, "arguments") ?? ""),
          status: "running",
          time: event.created_at,
        };
        lines.push(line);
        commands.set(callId, line);
        break;
      }
      case "tool.completed": {
        const data = asRecord(event.data);
        const tool = eventDataString(event, "tool") ?? "";
        const callId = eventDataString(event, "call_id") ?? "";
        let line = callId ? commands.get(callId) : undefined;
        if (!line) {
          for (let index = lines.length - 1; index >= 0; index -= 1) {
            const candidate = lines[index];
            if (
              candidate.kind === "command" &&
              candidate.tool === tool &&
              candidate.status === "running"
            ) {
              line = candidate;
              break;
            }
          }
        }
        const status: CommandStatus = data?.status === "failed" ? "failed" : "ok";
        if (line) {
          line.status = status;
        } else {
          // Preparation can fail before tool.started is published. Keep the
          // attempted call visible instead of dropping it from the console.
          lines.push({
            kind: "command",
            key: key(),
            callId,
            tool: tool || "tool",
            arguments: "",
            status,
            time: event.created_at,
          });
        }
        break;
      }
      case "model.failed": {
        const code = eventDataString(event, "code") ?? "model_failed";
        lines.push({
          kind: "error",
          key: key(),
          text: `模型调用失败：${code}`,
          time: event.created_at,
        });
        break;
      }
      default:
        break;
    }
  }

  if (terminal) {
    const status = terminalStatus(terminal);
    if (status) {
      lines.push({
        kind: "terminal",
        key: key(),
        status,
        code: eventDataString(terminal, "code"),
        time: terminal.created_at,
      });
    }
  }

  return {
    lines,
    report: reportParts.length ? reportParts.join("\n\n") : undefined,
  };
}
