"use client";

import { useMemo } from "react";
import { Card, Chip, EmptyState, Spinner } from "@heroui/react";

import {
  buildTimeline,
  eventDataNumber,
  eventDataString,
  type MarkerEntry,
  type ModelAttemptItem,
  type StepEntry,
  type ToolItem,
} from "@/lib/events";
import { eventTypeLabel, formatClock, statusLabel } from "@/lib/format";
import type { AgentEvent } from "@/lib/types";
import { CopyButton } from "./copy-button";

function ModelBlock({ item }: { item: ModelAttemptItem }) {
  const streaming = item.status === "streaming";
  return (
    <div className="rounded-xl border border-separator/50 bg-background/35 p-3">
      <div className="mb-2 flex flex-wrap items-center gap-2 text-[11px] text-muted">
        <span className="font-medium text-foreground/80">模型输出</span>
        {streaming ? (
          <span className="flex items-center gap-1 text-accent">
            <Spinner size="sm" color="current" />
            生成中
          </span>
        ) : item.status === "failed" ? (
          <Chip size="sm" color="danger" variant="soft">
            <Chip.Label>{item.code ?? "失败"}</Chip.Label>
          </Chip>
        ) : (
          <span className="text-success">完成</span>
        )}
        {item.startedAt ? <span>· {formatClock(item.startedAt)}</span> : null}
      </div>

      {item.text ? (
        <pre
          className={[
            "event-text font-sans text-sm leading-6 text-foreground/90",
            streaming ? "stream-cursor" : "",
          ].join(" ")}
        >
          {item.text}
        </pre>
      ) : (
        <p className="text-xs text-muted">
          {streaming ? "等待模型输出…" : "本步骤没有文字输出。"}
        </p>
      )}

      {item.argumentsText ? (
        <details className="mt-3 rounded-lg border border-separator/40 bg-surface/60 px-3 py-2">
          <summary className="cursor-pointer text-[11px] text-muted">
            工具调用参数 · {item.argumentsText.length} 字符
          </summary>
          <pre className="event-text mt-2 max-h-64 overflow-auto font-mono text-[11px] leading-5 text-foreground/75">
            {item.argumentsText}
          </pre>
        </details>
      ) : null}
    </div>
  );
}

function ToolBlock({ item }: { item: ToolItem }) {
  return (
    <div className="flex items-center justify-between gap-3 rounded-xl border border-separator/50 bg-surface/50 px-3 py-2">
      <div className="flex min-w-0 items-center gap-2">
        <span className="size-1.5 shrink-0 rounded-full bg-accent" />
        <span className="truncate font-mono text-[11px] text-foreground/85">{item.name}</span>
        <span className="shrink-0 text-[10px] text-muted">{formatClock(item.startedAt)}</span>
      </div>
      {item.status === "running" ? (
        <span className="flex shrink-0 items-center gap-1.5 text-[11px] text-accent">
          <Spinner size="sm" color="current" />
          执行中
        </span>
      ) : (
        <Chip
          size="sm"
          color={item.status === "failed" ? "danger" : "success"}
          variant="soft"
        >
          <Chip.Label>{item.status === "failed" ? "失败" : "完成"}</Chip.Label>
        </Chip>
      )}
    </div>
  );
}

function StepCard({ step }: { step: StepEntry }) {
  const failed = step.items.some(
    (item) =>
      (item.kind === "model" && item.status === "failed") ||
      (item.kind === "tool" && item.status === "failed"),
  );
  const allFinished =
    step.items.length > 0 && step.items.every((item) => item.status === "completed");
  const active =
    !step.completed &&
    step.items.some(
      (item) =>
        (item.kind === "model" && item.status === "streaming") ||
        (item.kind === "tool" && item.status === "running"),
    );
  const done = Boolean(step.completed) || allFinished;

  const contextVersion = step.completed
    ? eventDataNumber(step.completed, "context_version")
    : undefined;
  const eventSequence = step.completed
    ? eventDataNumber(step.completed, "event_sequence")
    : undefined;

  return (
    <Card className="border border-separator/50 bg-surface/45">
      <Card.Header className="flex-row flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <span className="rounded-md bg-default px-1.5 py-0.5 font-mono text-[10px] text-muted">
            STEP {step.stepId}
          </span>
          <Card.Title className="text-sm">模型步骤</Card.Title>
          {step.startedAt ? (
            <span className="text-[10px] text-muted">{formatClock(step.startedAt)}</span>
          ) : null}
        </div>
        <div className="flex items-center gap-2">
          {failed ? (
            <Chip size="sm" color="danger" variant="soft">
              <Chip.Label>有失败项</Chip.Label>
            </Chip>
          ) : done ? (
            <Chip size="sm" color="success" variant="soft">
              <Chip.Label>{step.completed ? "步骤完成" : "输出完成"}</Chip.Label>
            </Chip>
          ) : active ? (
            <span className="flex items-center gap-1.5 text-[11px] text-accent">
              <Spinner size="sm" color="current" />
              进行中
            </span>
          ) : (
            <Chip size="sm" color="default" variant="soft">
              <Chip.Label>等待中</Chip.Label>
            </Chip>
          )}
        </div>
      </Card.Header>
      <Card.Content className="space-y-2.5">
        {step.items.length === 0 ? (
          <p className="text-xs text-muted">正在准备步骤…</p>
        ) : (
          step.items.map((item) =>
            item.kind === "model" ? (
              <ModelBlock key={item.key} item={item} />
            ) : (
              <ToolBlock key={item.key} item={item} />
            ),
          )
        )}
        {step.completed ? (
          <p className="pt-1 text-[10px] text-muted">
            已提交 Context
            {contextVersion === undefined ? "" : ` v${contextVersion}`}
            {eventSequence === undefined ? "" : ` · 事件序号 ${eventSequence}`}
          </p>
        ) : null}
      </Card.Content>
    </Card>
  );
}

function TerminalCard({ event }: { event: AgentEvent }) {
  const status =
    event.type === "agent.completed"
      ? "completed"
      : event.type === "agent.failed"
        ? "failed"
        : "cancelled";
  const resultRef = eventDataString(event, "result_ref");
  const code = eventDataString(event, "code");
  const toneClass =
    status === "completed"
      ? "border-success/40 bg-success/5"
      : status === "failed"
        ? "border-danger/40 bg-danger/5"
        : "border-warning/40 bg-warning/5";

  return (
    <Card className={`border ${toneClass}`}>
      <Card.Header className="flex-row items-center justify-between gap-3">
        <div>
          <Card.Title className="text-sm">
            {status === "completed"
              ? "Agent 已完成"
              : status === "failed"
                ? "Agent 执行失败"
                : "Agent 已取消"}
          </Card.Title>
          <Card.Description className="text-xs">
            {formatClock(event.created_at)}
            {code ? ` · ${code}` : ""}
          </Card.Description>
        </div>
        <Chip
          size="sm"
          variant="soft"
          color={status === "completed" ? "success" : status === "failed" ? "danger" : "warning"}
        >
          <Chip.Label>{statusLabel(status)}</Chip.Label>
        </Chip>
      </Card.Header>
      {resultRef ? (
        <Card.Content className="flex flex-row flex-wrap items-center justify-start gap-2 text-left text-[11px] text-muted">
          <span className="shrink-0">结果引用</span>
          <span className="break-anywhere font-mono text-foreground/70">{resultRef}</span>
          <CopyButton value={resultRef} className="shrink-0" />
        </Card.Content>
      ) : null}
    </Card>
  );
}

function MarkerCard({ marker }: { marker: MarkerEntry }) {
  if (marker.kind === "terminal") {
    return <TerminalCard event={marker.event} />;
  }

  const status =
    marker.kind === "status" ? eventDataString(marker.event, "status") : undefined;
  const contextVersion =
    marker.kind === "compact"
      ? eventDataNumber(marker.event, "context_version")
      : undefined;
  const label =
    marker.kind === "compact"
      ? `Context 已压缩${contextVersion === undefined ? "" : ` → v${contextVersion}`}`
      : status
        ? `状态：${statusLabel(status)}`
        : eventTypeLabel(marker.event.type);

  return (
    <div className="flex items-center gap-3 py-0.5 text-[11px] text-muted">
      <span className="h-px flex-1 bg-separator/50" />
      <span className="flex shrink-0 items-center gap-1.5">
        <span
          className={[
            "size-1.5 rounded-full",
            marker.kind === "compact"
              ? "bg-warning"
              : status === "running"
                ? "bg-accent"
                : "bg-muted",
          ].join(" ")}
        />
        {label} · {formatClock(marker.event.created_at)}
      </span>
      <span className="h-px flex-1 bg-separator/50" />
    </div>
  );
}

export function EventTimeline({ events }: { events: AgentEvent[] }) {
  const timeline = useMemo(() => buildTimeline(events), [events]);

  if (timeline.length === 0) {
    return (
      <EmptyState className="px-6 py-16 text-center">
        <p className="text-sm font-medium">还没有可显示的事件</p>
        <p className="mt-1 text-xs text-muted">
          如果任务已经结束，事件历史可能已超过 Redis 保留期；最终结果仍可在右侧查看。
        </p>
      </EmptyState>
    );
  }

  return (
    <div className="space-y-3">
      {timeline.map((entry) =>
        entry.kind === "step" ? (
          <StepCard key={entry.key} step={entry} />
        ) : (
          <MarkerCard key={entry.key} marker={entry} />
        ),
      )}
    </div>
  );
}
