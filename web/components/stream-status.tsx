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
}: {
  state: StreamState;
  error: StreamError | null;
}) {
  return (
    <div
      className="flex items-center gap-1.5 text-[11px] text-muted"
      title={error?.message}
      aria-live="polite"
    >
      <StateIcon state={state} />
      {LABELS[state]}
    </div>
  );
}
