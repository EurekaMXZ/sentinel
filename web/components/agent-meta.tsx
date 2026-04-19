"use client";

import { useEffect, useState } from "react";
import { Card, Chip } from "@heroui/react";

import { formatDateTime, formatDuration } from "@/lib/format";
import { findRecent } from "@/lib/storage";
import type { AgentView, RecentAnalysis } from "@/lib/types";
import { CopyButton } from "./copy-button";
import { StatusChip } from "./status-chip";

function Row({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div className="grid grid-cols-[78px_minmax(0,1fr)] gap-2 text-[11px] leading-5">
      <dt className="text-muted">{label}</dt>
      <dd className="min-w-0 break-anywhere text-foreground/80">{children}</dd>
    </div>
  );
}

function IdRow({ label, value }: { label: string; value: string }) {
  return (
    <Row label={label}>
      <span className="font-mono text-[10px]">{value}</span>
    </Row>
  );
}

export function AgentMeta({
  agent,
  displayStatus,
}: {
  agent: AgentView;
  displayStatus: string;
}) {
  const [recent, setRecent] = useState<RecentAnalysis | null>(null);

  useEffect(() => {
    setRecent(findRecent(agent.agent_id) ?? null);
  }, [agent.agent_id]);

  return (
    <Card className="border border-separator/50">
      <Card.Header className="flex-row items-center justify-between gap-2">
        <Card.Title className="text-sm">任务信息</Card.Title>
        <StatusChip status={displayStatus} />
      </Card.Header>
      <Card.Content className="space-y-3">
        <dl className="space-y-1.5">
          <Row label="Agent">
            <div className="flex min-w-0 items-start gap-1.5">
              <span className="min-w-0 break-anywhere font-mono text-[10px]">
                {agent.agent_id}
              </span>
              <CopyButton value={agent.agent_id} className="shrink-0" />
            </div>
          </Row>
          <Row label="Workflow">
            <div className="flex min-w-0 items-start gap-1.5">
              <span className="min-w-0 break-anywhere font-mono text-[10px]">
                {agent.workflow_id}
              </span>
              <CopyButton value={agent.workflow_id} className="shrink-0" />
            </div>
          </Row>
          <IdRow label="Session" value={agent.session_id} />
          {agent.parent_agent_id ? (
            <IdRow label="父 Agent" value={agent.parent_agent_id} />
          ) : null}
          <Row label="创建">{formatDateTime(agent.created_at)}</Row>
          <Row label="开始">{formatDateTime(agent.started_at)}</Row>
          <Row label="结束">{formatDateTime(agent.finished_at)}</Row>
          <Row label="耗时">
            {formatDuration(agent.started_at ?? agent.created_at, agent.finished_at)}
          </Row>
          <Row label="Context">v{agent.context_version}</Row>
          <Row label="事件序号">{agent.last_event_sequence}</Row>
          <Row label="下一步骤">{agent.next_step_id}</Row>
        </dl>

        {agent.resources?.length ? (
          <div>
            <p className="mb-1.5 text-[11px] font-medium text-muted">资源绑定</p>
            <div className="space-y-1.5">
              {agent.resources.map((resource) => (
                <div
                  key={resource.binding_name}
                  className="rounded-lg border border-separator/40 bg-surface/50 px-2.5 py-2"
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="truncate font-mono text-[10px] text-foreground/80">
                      {resource.binding_name}
                    </span>
                    <Chip size="sm" variant="soft" color="default">
                      <Chip.Label>{resource.resource_kind}</Chip.Label>
                    </Chip>
                  </div>
                  {resource.repository_id ? (
                    <p className="mt-1 break-anywhere font-mono text-[9px] text-muted">
                      repo {resource.repository_id}
                    </p>
                  ) : null}
                  {resource.source_snapshot_id ? (
                    <p className="break-anywhere font-mono text-[9px] text-muted">
                      snap {resource.source_snapshot_id}
                    </p>
                  ) : null}
                  {resource.object_id ? (
                    <p className="break-anywhere font-mono text-[9px] text-muted">
                      object {resource.object_id}
                    </p>
                  ) : null}
                </div>
              ))}
            </div>
          </div>
        ) : (
          <p className="text-[11px] text-muted">没有绑定源码资源。</p>
        )}

        {recent ? (
          <details className="rounded-lg border border-separator/40 bg-surface/50 px-2.5 py-2">
            <summary className="cursor-pointer text-[11px] text-muted">创建时的请求</summary>
            <p className="mt-2 break-anywhere text-[10px] text-foreground/70">{recent.url}</p>
            <p className="mt-1 text-[10px] leading-5 text-muted">{recent.task}</p>
          </details>
        ) : null}
      </Card.Content>
    </Card>
  );
}
