"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import {
  Alert,
  Button,
  Card,
  EmptyState,
  Skeleton,
  Table,
} from "@heroui/react";

import { api } from "@/lib/api";
import { shortId } from "@/lib/config";
import { formatRelative } from "@/lib/format";
import { loadRecent } from "@/lib/storage";
import type { AgentView, RecentAnalysis } from "@/lib/types";
import { StatusChip } from "./status-chip";

function repoLabel(url: string | undefined): string {
  if (!url) return "";
  try {
    const parsed = new URL(url);
    const parts = parsed.pathname.replace(/^\/|\/+$/g, "").split("/");
    return parts.length >= 2 ? `${parts[0]}/${parts[1].replace(/\.git$/, "")}` : parsed.pathname;
  } catch {
    return url;
  }
}

export function RecentTasks() {
  const [agents, setAgents] = useState<AgentView[]>([]);
  const [recent, setRecent] = useState<RecentAnalysis[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const response = await api.listAgents(20);
      setAgents(response.agents ?? []);
      setRecent(loadRecent());
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "加载任务列表失败");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
    const onFocus = () => void load();
    window.addEventListener("focus", onFocus);
    return () => window.removeEventListener("focus", onFocus);
  }, [load]);

  return (
    <section id="recent" className="mt-12 scroll-mt-24">
      <div className="mb-3 flex items-end justify-between gap-3">
        <div>
          <h2 className="text-lg font-semibold tracking-tight">最近任务</h2>
          <p className="mt-0.5 text-xs text-muted">当前租户最近创建的 20 个 Agent</p>
        </div>
        <Button variant="ghost" size="sm" onPress={() => void load()} isDisabled={loading}>
          刷新
        </Button>
      </div>

      <Card>
        <Card.Content className="p-0">
          {loading && agents.length === 0 ? (
            <div className="space-y-2 p-4">
              <Skeleton className="h-11 w-full rounded-lg" />
              <Skeleton className="h-11 w-full rounded-lg" />
              <Skeleton className="h-11 w-full rounded-lg" />
            </div>
          ) : error ? (
            <div className="p-4">
              <Alert status="danger">
                <Alert.Indicator>!</Alert.Indicator>
                <Alert.Content>
                  <Alert.Title>无法加载最近任务</Alert.Title>
                  <Alert.Description>{error}</Alert.Description>
                </Alert.Content>
              </Alert>
            </div>
          ) : agents.length === 0 ? (
            <EmptyState className="px-6 py-12 text-center">
              <p className="text-sm font-medium">还没有分析任务</p>
              <p className="mt-1 text-xs text-muted">在上方输入 GitHub 仓库地址开始第一次分析。</p>
            </EmptyState>
          ) : (
            <Table>
              <Table.ScrollContainer>
                <Table.Content aria-label="最近分析任务">
                  <Table.Header>
                    <Table.Column isRowHeader>状态</Table.Column>
                    <Table.Column>任务</Table.Column>
                    <Table.Column>Agent</Table.Column>
                    <Table.Column>创建时间</Table.Column>
                    <Table.Column> </Table.Column>
                  </Table.Header>
                  <Table.Body>
                    {agents.map((agent) => {
                      const meta = recent.find((item) => item.agentId === agent.agent_id);
                      const repo = repoLabel(meta?.url);
                      return (
                        <Table.Row key={agent.agent_id}>
                          <Table.Cell>
                            <StatusChip status={agent.status} />
                          </Table.Cell>
                          <Table.Cell>
                            <div className="min-w-0">
                              <p className="truncate text-sm font-medium">
                                {repo || `Agent ${shortId(agent.agent_id, 12)}`}
                              </p>
                              <p className="max-w-[520px] truncate text-xs text-muted">
                                {meta?.task || agent.workflow_id}
                              </p>
                            </div>
                          </Table.Cell>
                          <Table.Cell>
                            <span className="font-mono text-xs text-muted">
                              {shortId(agent.agent_id, 12)}
                            </span>
                          </Table.Cell>
                          <Table.Cell>
                            <span className="text-xs text-muted" title={agent.created_at}>
                              {formatRelative(agent.created_at)}
                            </span>
                          </Table.Cell>
                          <Table.Cell>
                            <Link
                              href={`/tasks/${agent.agent_id}`}
                              className="whitespace-nowrap text-sm font-medium text-accent hover:underline"
                            >
                              查看事件日志 →
                            </Link>
                          </Table.Cell>
                        </Table.Row>
                      );
                    })}
                  </Table.Body>
                </Table.Content>
              </Table.ScrollContainer>
            </Table>
          )}
        </Card.Content>
      </Card>
    </section>
  );
}
