"use client";

import {
  Activity,
  CircleAlert,
  CircleCheck,
  LoaderCircle,
  RefreshCw,
} from "lucide-react";

import type { StreamError, StreamState } from "@/lib/use-agent-stream";

const LABELS: Record<StreamState, string> = {
  connecting: "连接中",
  streaming: "实时",
  reconnecting: "重连中",
  closed: "已结束",
  error: "不可用",
};

function StateIcon({ state }: { state: StreamState }) {
  switch (state) {
    case "connecting":
      return <LoaderCircle size={12} className="animate-spin text-accent" />;
    case "streaming":
      return <Activity size={12} className="text-success" />;
    case "reconnecting":
      return <RefreshCw size={12} className="animate-spin text-warning" />;
    case "closed":
      return <CircleCheck size={12} className="text-muted" />;
    case "error":
      return <CircleAlert size={12} className="text-danger" />;
  }
}

export function StreamStatus({
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
  const title =
    error?.message ??
    `${eventCount} 条事件${lastEventId ? ` · 游标 ${lastEventId}` : ""}`;

  return (
    <div
      className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted"
      title={title}
      aria-live="polite"
    >
      <span className="flex items-center gap-1.5">
        <StateIcon state={state} />
        {LABELS[state]}
      </span>
      <span className="text-separator">·</span>
      <span>{eventCount} 条事件</span>
    </div>
  );
}
