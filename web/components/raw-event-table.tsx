"use client";

import { useMemo, useState } from "react";
import { Button, Card, Chip, EmptyState } from "@heroui/react";

import { eventSummary } from "@/lib/events";
import { eventTone, eventTypeLabel, formatClock } from "@/lib/format";
import type { AgentEvent } from "@/lib/types";
import { CopyButton } from "./copy-button";

const PAGE_SIZE = 200;

export function RawEventTable({ events }: { events: AgentEvent[] }) {
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [visibleCount, setVisibleCount] = useState(PAGE_SIZE);

  const visible = useMemo(() => {
    const start = Math.max(0, events.length - visibleCount);
    return events.slice(start);
  }, [events, visibleCount]);

  const selected = useMemo(
    () => events.find((event) => event.id === selectedId) ?? null,
    [events, selectedId],
  );

  if (events.length === 0) {
    return (
      <EmptyState className="px-6 py-16 text-center">
        <p className="text-sm font-medium">没有匹配的事件</p>
        <p className="mt-1 text-xs text-muted">调整筛选条件，或等待新的流式事件。</p>
      </EmptyState>
    );
  }

  return (
    <div>
      <div className="mb-2 flex items-center justify-between gap-3 text-[11px] text-muted">
        <span>
          显示最近 {visible.length} / {events.length} 条事件
        </span>
        {events.length > visible.length ? (
          <Button
            variant="ghost"
            size="sm"
            onPress={() => setVisibleCount((count) => count + PAGE_SIZE)}
          >
            加载更早
          </Button>
        ) : null}
      </div>

      <div className="overflow-x-auto rounded-xl border border-separator/50">
        <table className="w-full border-collapse text-left text-xs">
          <thead className="sticky top-0 z-10 bg-surface">
            <tr className="border-b border-separator/60 text-[10px] uppercase tracking-wide text-muted">
              <th className="whitespace-nowrap px-3 py-2 font-medium">时间</th>
              <th className="whitespace-nowrap px-3 py-2 font-medium">类型</th>
              <th className="whitespace-nowrap px-3 py-2 font-medium">Step</th>
              <th className="whitespace-nowrap px-3 py-2 font-medium">Attempt</th>
              <th className="min-w-[220px] px-3 py-2 font-medium">摘要</th>
              <th className="whitespace-nowrap px-3 py-2 font-medium">Stream ID</th>
            </tr>
          </thead>
          <tbody>
            {visible.map((event) => {
              const active = event.id === selectedId;
              return (
                <tr
                  key={event.id}
                  onClick={() => setSelectedId(active ? null : event.id)}
                  className={[
                    "cursor-pointer border-b border-separator/30 transition-colors last:border-0",
                    active ? "bg-accent/10" : "hover:bg-default/40",
                  ].join(" ")}
                >
                  <td className="whitespace-nowrap px-3 py-2 font-mono text-[10px] text-muted">
                    {formatClock(event.created_at)}
                  </td>
                  <td className="whitespace-nowrap px-3 py-2">
                    <Chip size="sm" variant="soft" color={eventTone(event.type)}>
                      <Chip.Label>{eventTypeLabel(event.type)}</Chip.Label>
                    </Chip>
                  </td>
                  <td className="px-3 py-2 font-mono text-[10px] text-muted">
                    {event.step_id ?? "—"}
                  </td>
                  <td className="px-3 py-2 font-mono text-[10px] text-muted">
                    {event.attempt ?? "—"}
                  </td>
                  <td className="max-w-[420px] truncate px-3 py-2 text-foreground/80">
                    {eventSummary(event) || <span className="text-muted">—</span>}
                  </td>
                  <td className="whitespace-nowrap px-3 py-2 font-mono text-[10px] text-muted">
                    {event.id}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      {selected ? (
        <Card className="mt-3 border border-separator/50">
          <Card.Header className="flex-row items-center justify-between gap-2">
            <Card.Title className="text-sm">原始事件</Card.Title>
            <div className="flex items-center gap-2">
              <span className="font-mono text-[10px] text-muted">{selected.id}</span>
              <CopyButton value={JSON.stringify(selected, null, 2)} label="复制 JSON" />
            </div>
          </Card.Header>
          <Card.Content>
            <pre className="event-text max-h-96 overflow-auto rounded-lg bg-background/60 p-3 font-mono text-[11px] leading-5 text-foreground/80">
              {JSON.stringify(selected, null, 2)}
            </pre>
          </Card.Content>
        </Card>
      ) : null}
    </div>
  );
}
