"use client";

import { Chip, Spinner } from "@heroui/react";

import type { StreamError, StreamState } from "@/lib/use-agent-stream";

const LABELS: Record<StreamState, string> = {
  connecting: "连接中",
  streaming: "事件流已连接",
  reconnecting: "重连中",
  closed: "事件流已结束",
  error: "事件流不可用",
};

const COLORS: Record<StreamState, "default" | "accent" | "success" | "warning" | "danger"> = {
  connecting: "accent",
  streaming: "success",
  reconnecting: "warning",
  closed: "default",
  error: "danger",
};

export function ConnectionBadge({
  state,
  error,
  eventCount,
  lastEventId,
}: {
  state: StreamState;
  error: StreamError | null;
  eventCount: number;
  lastEventId: string;
}) {
  const busy = state === "connecting" || state === "streaming" || state === "reconnecting";

  return (
    <div className="flex flex-wrap items-center gap-2 text-[11px] text-muted">
      <Chip size="sm" variant="soft" color={COLORS[state]} title={error?.message}>
        <Chip.Label>
          <span className="flex items-center gap-1.5">
            {busy ? <Spinner size="sm" color="current" /> : null}
            {LABELS[state]}
          </span>
        </Chip.Label>
      </Chip>
      <span>{eventCount} 条事件</span>
      {lastEventId ? (
        <span className="font-mono text-[10px] text-muted/80">游标 {lastEventId}</span>
      ) : null}
      {error && state !== "error" ? (
        <span className="max-w-[420px] truncate text-[10px]" title={error.message}>
          {error.message}
        </span>
      ) : null}
    </div>
  );
}
