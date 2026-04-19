"use client";

import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { Button, Chip, Skeleton } from "@heroui/react";
import {
  CircleAlert,
  CircleCheck,
  CircleSlash,
  Clock,
  LoaderCircle,
  Plus,
  RefreshCw,
  ScanSearch,
} from "lucide-react";

import { TENANT_ID, shortId } from "@/lib/config";
import { formatRelative, repositoryLabel } from "@/lib/format";
import type { AgentStatus } from "@/lib/types";
import { useRecentAgents } from "@/lib/use-recent-agents";

function StatusIcon({ status }: { status: AgentStatus }) {
  switch (status) {
    case "running":
      return <LoaderCircle size={14} className="animate-spin text-accent" />;
    case "completed":
      return <CircleCheck size={14} className="text-success" />;
    case "failed":
      return <CircleAlert size={14} className="text-danger" />;
    case "cancelled":
      return <CircleSlash size={14} className="text-muted" />;
    default:
      return <Clock size={14} className="text-warning" />;
  }
}

export function SidebarContent({
  onNavigate,
  className = "",
}: {
  onNavigate?: () => void;
  className?: string;
}) {
  const pathname = usePathname();
  const router = useRouter();
  const { agents, recent, repositoryURLs, loading, error, reload } = useRecentAgents(8);

  function openNewAnalysis() {
    onNavigate?.();
    router.push("/");
  }

  return (
    <div className={`flex h-full min-h-0 flex-col p-4 ${className}`}>
      <Link
        href="/"
        onClick={onNavigate}
        className="flex items-center gap-2.5 rounded-xl px-1 py-1"
      >
        <span className="grid size-9 place-items-center rounded-xl bg-accent text-accent-foreground shadow-surface">
          <ScanSearch size={18} />
        </span>
        <span className="leading-tight">
          <span className="block text-sm font-semibold tracking-tight">Sentinel</span>
          <span className="block text-[11px] text-muted">只读源码分析</span>
        </span>
      </Link>

      <Button
        variant="primary"
        fullWidth
        className="mt-5 justify-start gap-2"
        onPress={openNewAnalysis}
      >
        <Plus size={16} />
        新建分析
      </Button>

      <div className="mt-6 flex min-h-0 flex-1 flex-col">
        <div className="flex items-center justify-between px-2">
          <span className="text-[11px] font-medium uppercase tracking-wider text-muted">
            最近任务
          </span>
          <Button
            isIconOnly
            variant="ghost"
            size="sm"
            aria-label="刷新最近任务"
            onPress={reload}
          >
            <RefreshCw size={13} />
          </Button>
        </div>

        <div className="mt-1 min-h-0 flex-1 space-y-0.5 overflow-y-auto pr-0.5">
          {loading && agents.length === 0 ? (
            <div className="space-y-1.5 px-2 pt-2">
              {[0, 1, 2, 3].map((item) => (
                <Skeleton key={item} className="h-10 w-full rounded-lg" />
              ))}
            </div>
          ) : error && agents.length === 0 ? (
            <div className="px-2 pt-2 text-[11px] text-muted">
              <p className="flex items-center gap-1.5 text-danger">
                <CircleAlert size={12} />
                无法加载任务
              </p>
              <Button
                variant="ghost"
                size="sm"
                className="mt-1 h-6 px-1.5 text-[10px]"
                onPress={reload}
              >
                重试
              </Button>
            </div>
          ) : agents.length === 0 ? (
            <p className="px-2 pt-2 text-[11px] text-muted">还没有分析任务</p>
          ) : (
            agents.map((agent) => {
              const meta = recent.find((item) => item.agentId === agent.agent_id);
              const repositoryID = agent.resources?.find(
                (resource) => resource.repository_id,
              )?.repository_id;
              const label =
                repositoryLabel(meta?.url) ||
                repositoryLabel(repositoryID ? repositoryURLs[repositoryID] : "") ||
                meta?.task ||
                `Agent ${shortId(agent.agent_id, 10)}`;
              const active = pathname === `/tasks/${agent.agent_id}`;
              return (
                <Link
                  key={agent.agent_id}
                  href={`/tasks/${agent.agent_id}`}
                  onClick={onNavigate}
                  aria-current={active ? "page" : undefined}
                  className={[
                    "flex items-center gap-2.5 rounded-lg px-2 py-2 transition-colors",
                    active
                      ? "bg-default text-foreground"
                      : "text-muted hover:bg-default/50 hover:text-foreground",
                  ].join(" ")}
                >
                  <StatusIcon status={agent.status} />
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-xs font-medium">{label}</span>
                    <span className="block text-[10px] text-muted">
                      {formatRelative(agent.created_at)}
                    </span>
                  </span>
                </Link>
              );
            })
          )}
        </div>
      </div>

      <div className="mt-4 border-t border-separator/60 pt-3">
        <Chip
          size="sm"
          variant="soft"
          color="default"
          title={TENANT_ID}
          className="max-w-full font-mono"
        >
          <Chip.Label>租户 {shortId(TENANT_ID)}</Chip.Label>
        </Chip>
      </div>
    </div>
  );
}

export function AppSidebar() {
  return (
    <aside className="sticky top-0 hidden h-dvh w-[248px] shrink-0 border-r border-separator/60 bg-surface/20 lg:block">
      <SidebarContent />
    </aside>
  );
}
