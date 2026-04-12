"use client";

import {
  Alert,
  Button,
  Card,
  Chip,
  Skeleton,
} from "@heroui/react";
import { Streamdown } from "streamdown";

import { isAgentTerminal, statusLabel } from "@/lib/format";
import type { AgentResult } from "@/lib/types";
import { CopyButton } from "./copy-button";

export function ResultPanel({
  status,
  result,
  loading,
  error,
  onRetry,
}: {
  status: string | undefined;
  result: AgentResult | null;
  loading: boolean;
  error: string | null;
  onRetry: () => void;
}) {
  const terminal = isAgentTerminal(status);

  return (
    <Card className="border border-separator/50">
      <Card.Header className="flex-row items-center justify-between gap-2">
        <div>
          <Card.Title className="text-sm">最终结果</Card.Title>
          <Card.Description className="text-[11px]">
            {terminal ? statusLabel(status) : "任务完成后显示"}
          </Card.Description>
        </div>
        <Chip size="sm" variant="soft" color={terminal ? "success" : "default"}>
          <Chip.Label>{terminal ? "已结束" : "进行中"}</Chip.Label>
        </Chip>
      </Card.Header>
      <Card.Content className="space-y-3">
        {!terminal ? (
          <p className="text-xs leading-5 text-muted">
            Agent 仍在运行。完成、失败或取消后，这里会显示持久化结果。
          </p>
        ) : loading ? (
          <div className="space-y-2">
            <Skeleton className="h-4 w-3/4 rounded" />
            <Skeleton className="h-4 w-full rounded" />
            <Skeleton className="h-4 w-5/6 rounded" />
          </div>
        ) : error ? (
          <Alert status="danger">
            <Alert.Indicator>!</Alert.Indicator>
            <Alert.Content>
              <Alert.Title>结果读取失败</Alert.Title>
              <Alert.Description>{error}</Alert.Description>
              <div className="mt-3">
                <Button variant="secondary" size="sm" onPress={onRetry}>
                  重试
                </Button>
              </div>
            </Alert.Content>
          </Alert>
        ) : result ? (
          <>
            {result.summary ? (
              <Streamdown
                mode="static"
                animated={false}
                className="streamdown max-w-none text-sm leading-6 text-foreground/85"
              >
                {result.summary}
              </Streamdown>
            ) : (
              <p className="text-xs text-muted">Agent 没有返回文字摘要。</p>
            )}

            {result.error ? (
              <details className="rounded-lg border border-danger/30 bg-danger/5 px-3 py-2">
                <summary className="cursor-pointer text-[11px] text-danger">错误详情</summary>
                <pre className="event-text mt-2 max-h-48 overflow-auto font-mono text-[10px] leading-5 text-foreground/70">
                  {JSON.stringify(result.error, null, 2)}
                </pre>
              </details>
            ) : null}

            {result.source_refs?.length ? (
              <div>
                <p className="mb-1.5 text-[11px] font-medium text-muted">源码证据版本</p>
                <div className="space-y-1.5">
                  {result.source_refs.map((reference) => (
                    <div
                      key={`${reference.repository_id}-${reference.source_snapshot_id}`}
                      className="rounded-lg border border-separator/40 bg-surface/50 px-2.5 py-2 text-[10px]"
                    >
                      <p className="font-mono text-foreground/70">
                        repo {reference.repository_id}
                      </p>
                      <p className="mt-0.5 font-mono text-muted">
                        snap {reference.source_snapshot_id}
                      </p>
                    </div>
                  ))}
                </div>
              </div>
            ) : null}

            {result.artifacts?.length ? (
              <details className="rounded-lg border border-separator/40 bg-surface/50 px-3 py-2">
                <summary className="cursor-pointer text-[11px] text-muted">
                  Artifacts · {result.artifacts.length} 项
                </summary>
                <pre className="event-text mt-2 max-h-48 overflow-auto font-mono text-[10px] leading-5 text-foreground/70">
                  {JSON.stringify(result.artifacts, null, 2)}
                </pre>
              </details>
            ) : null}

            {result.result_ref ? (
              <div className="flex items-center gap-2 text-[10px] text-muted">
                <span className="shrink-0">result_ref</span>
                <span className="break-anywhere font-mono text-foreground/60">
                  {result.result_ref}
                </span>
                <CopyButton value={result.result_ref} />
              </div>
            ) : null}
          </>
        ) : (
          <div className="text-xs text-muted">
            <p>Agent 已结束，但结果尚未加载。</p>
            <Button className="mt-2" variant="secondary" size="sm" onPress={onRetry}>
              加载结果
            </Button>
          </div>
        )}
      </Card.Content>
    </Card>
  );
}
