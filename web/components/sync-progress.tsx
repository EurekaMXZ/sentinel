import { ProgressBar } from "@heroui/react";

import type { AnalysisFlow } from "@/lib/types";
import { statusLabel } from "@/lib/format";

const SYNC_PROGRESS: Record<string, number> = {
  queued: 8,
  running: 35,
  archiving: 65,
  publishing: 85,
  succeeded: 100,
};

function progressFor(status: string | undefined): number {
  return status ? (SYNC_PROGRESS[status] ?? 15) : 0;
}

export function SyncProgress({ flow }: { flow: AnalysisFlow }) {
  const steps = [
    {
      label: "注册仓库",
      hint: flow.repositoryId ? "已就绪" : "等待提交",
      done: Boolean(flow.repositoryId),
      active: flow.phase === "registering",
    },
    {
      label: "同步源码",
      hint: flow.syncStatus ? statusLabel(flow.syncStatus) : "等待开始",
      done: Boolean(flow.snapshot || flow.agentId),
      active: flow.phase === "syncing",
    },
    {
      label: "选择版本",
      hint: flow.snapshot ? `commit ${flow.snapshot.commit_sha.slice(0, 8)}` : "等待快照",
      done: Boolean(flow.snapshot || flow.agentId),
      active: flow.phase === "selecting",
    },
    {
      label: "创建任务",
      hint: flow.agentId ? "Agent 已创建" : "等待调度",
      done: Boolean(flow.agentId),
      active: flow.phase === "creating",
    },
  ];

  return (
    <div>
      <ol className="grid gap-3 sm:grid-cols-4">
        {steps.map((step, index) => (
          <li key={step.label} className="flex items-start gap-2.5">
            <span
              className={[
                "mt-0.5 grid size-6 shrink-0 place-items-center rounded-full border text-[11px] font-semibold",
                step.done
                  ? "border-success bg-success text-success-foreground"
                  : step.active
                    ? "border-accent bg-accent text-accent-foreground"
                    : "border-separator text-muted",
              ].join(" ")}
            >
              {step.done ? "✓" : index + 1}
            </span>
            <div className="min-w-0">
              <p className="text-xs font-medium text-foreground">{step.label}</p>
              <p className="truncate text-[11px] text-muted">{step.hint}</p>
            </div>
          </li>
        ))}
      </ol>

      {flow.phase === "syncing" || flow.phase === "selecting" ? (
        <ProgressBar
          aria-label="源码同步进度"
          value={progressFor(flow.syncStatus)}
          color="accent"
          size="sm"
          className="mt-4"
        >
          <ProgressBar.Track>
            <ProgressBar.Fill />
          </ProgressBar.Track>
        </ProgressBar>
      ) : null}
    </div>
  );
}
