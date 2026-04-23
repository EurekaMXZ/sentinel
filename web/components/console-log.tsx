import { formatClock } from "@/lib/format";
import type { ConsoleLine } from "@/lib/console";

function Time({ value }: { value: string }) {
  return (
    <span className="w-[58px] shrink-0 text-muted/60">{formatClock(value)}</span>
  );
}

function Line({ line }: { line: ConsoleLine }) {
  switch (line.kind) {
    case "step":
      return (
        <div className="mt-2 flex items-center gap-2 border-t border-separator/30 pt-2 text-muted first:mt-0 first:border-0 first:pt-0">
          <Time value={line.time} />
          <span>[step {line.stepId}]</span>
        </div>
      );
    case "command": {
      const command = `$ ${line.tool}${line.arguments ? ` ${line.arguments}` : ""} [${line.status}]`;
      return (
        <div className="flex items-start gap-2 whitespace-nowrap" title={command}>
          <Time value={line.time} />
          <span className="text-accent">$</span>
          <span className="text-foreground/90">{line.tool}</span>
          {line.arguments ? (
            <span className="text-muted">{line.arguments}</span>
          ) : null}
          <span
            className={
              line.status === "failed"
                ? "text-danger"
                : line.status === "ok"
                  ? "text-success"
                  : "text-warning"
            }
          >
            [{line.status}]
          </span>
        </div>
      );
    }
    case "output":
      return (
        <div className="flex items-start gap-2 whitespace-nowrap" title={line.text}>
          <Time value={line.time} />
          <span className="text-muted">&gt;</span>
          <span className="text-foreground/80">{line.text}</span>
        </div>
      );
    case "error":
      return (
        <div
          className="flex items-start gap-2 whitespace-nowrap text-danger"
          title={line.text}
        >
          <Time value={line.time} />
          <span>!</span>
          <span>{line.text}</span>
        </div>
      );
    case "terminal":
      return (
        <div className="mt-2 flex items-center gap-2 border-t border-separator/30 pt-2 whitespace-nowrap">
          <Time value={line.time} />
          <span
            className={
              line.status === "completed"
                ? "text-success"
                : line.status === "failed"
                  ? "text-danger"
                  : "text-warning"
            }
          >
            [{line.status}]
          </span>
          {line.code ? <span className="text-muted">{line.code}</span> : null}
        </div>
      );
  }
}

export function ConsoleLog({
  lines,
  emptyLabel = "等待事件…",
}: {
  lines: ConsoleLine[];
  emptyLabel?: string;
}) {
  if (lines.length === 0) {
    return (
      <div className="rounded-xl border border-separator/60 bg-black/30 p-4 font-mono text-xs text-muted">
        {emptyLabel}
      </div>
    );
  }

  return (
    <div
      className="overflow-hidden rounded-xl border border-separator/60 bg-black/30"
      role="log"
      aria-label="执行日志"
    >
      <div className="overflow-x-auto p-3 font-mono text-xs">
        <div className="min-w-max space-y-1">
          {lines.map((line) => (
            <Line key={line.key} line={line} />
          ))}
        </div>
      </div>
    </div>
  );
}
