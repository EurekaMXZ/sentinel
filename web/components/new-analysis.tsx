"use client";

import { useState } from "react";
import Link from "next/link";
import {
  Accordion,
  Alert,
  Button,
  Card,
  Chip,
  Input,
  Spinner,
  TextArea,
} from "@heroui/react";

import { DEFAULT_TASK, shortId } from "@/lib/config";
import { statusLabel } from "@/lib/format";
import type { FlowPhase } from "@/lib/types";
import { useAnalysisFlow } from "@/lib/use-analysis-flow";
import { SyncProgress } from "./sync-progress";
import { CopyButton } from "./copy-button";
import { RecentTasks } from "./recent-tasks";

const PHASE_LABEL: Record<FlowPhase, string> = {
  idle: "待开始",
  registering: "注册仓库",
  syncing: "同步源码",
  selecting: "选择版本",
  creating: "创建任务",
  done: "已创建",
  error: "需要处理",
};

function SavedFlowCard({
  url,
  phase,
  agentId,
  onResume,
  onDismiss,
}: {
  url: string;
  phase: FlowPhase;
  agentId?: string;
  onResume: () => void;
  onDismiss: () => void;
}) {
  return (
    <Card className="mt-4 border-accent/30">
      <Card.Content className="flex flex-col gap-3 p-4 sm:flex-row sm:items-center sm:justify-between">
        <div className="min-w-0">
          <p className="text-sm font-medium">上次分析尚未完成</p>
          <p className="mt-0.5 truncate text-xs text-muted">{url}</p>
          <p className="mt-1 text-[11px] text-muted">
            阶段：{PHASE_LABEL[phase]}
            {agentId ? ` · Agent ${shortId(agentId)}` : ""}
          </p>
        </div>
        <div className="flex shrink-0 gap-2">
          <Button variant="ghost" size="sm" onPress={onDismiss}>
            忽略
          </Button>
          <Button variant="primary" size="sm" onPress={onResume}>
            {agentId ? "打开任务" : "继续"}
          </Button>
        </div>
      </Card.Content>
    </Card>
  );
}

function FlowPanel({
  flow,
  onRetry,
  onReset,
}: {
  flow: ReturnType<typeof useAnalysisFlow>["flow"];
  onRetry: () => void;
  onReset: () => void;
}) {
  const error = flow.error;
  const agentLink = flow.agentId ?? error?.agentId;

  return (
    <Card className="mt-4 border-accent/20">
      <Card.Header className="flex-row flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <Card.Title className="text-base">分析流程</Card.Title>
          <Card.Description className="truncate text-xs">{flow.url}</Card.Description>
        </div>
        <div className="flex items-center gap-2">
          <Chip
            size="sm"
            variant="soft"
            color={flow.phase === "error" ? "danger" : flow.phase === "done" ? "success" : "accent"}
          >
            <Chip.Label>{PHASE_LABEL[flow.phase]}</Chip.Label>
          </Chip>
          <Button variant="ghost" size="sm" onPress={onReset}>
            清除
          </Button>
        </div>
      </Card.Header>
      <Card.Content className="space-y-4">
        <SyncProgress flow={flow} />

        <div className="grid gap-2 text-xs text-muted sm:grid-cols-2">
          {flow.repositoryId ? (
            <div className="flex min-w-0 items-center gap-1.5">
              <span className="shrink-0">仓库</span>
              <span className="truncate font-mono text-foreground/80">{flow.repositoryId}</span>
              <CopyButton value={flow.repositoryId} label="复制" />
            </div>
          ) : null}
          {flow.syncId ? (
            <div className="flex min-w-0 items-center gap-1.5">
              <span className="shrink-0">同步</span>
              <span className="truncate font-mono text-foreground/80">{flow.syncId}</span>
              <CopyButton value={flow.syncId} label="复制" />
            </div>
          ) : null}
          {flow.syncStatus ? (
            <div>
              <span>同步状态：</span>
              <span className="text-foreground/80">{statusLabel(flow.syncStatus)}</span>
            </div>
          ) : null}
          {flow.snapshot ? (
            <div className="break-anywhere">
              <span>版本：</span>
              <span className="font-mono text-foreground/80">
                {flow.snapshot.commit_sha.slice(0, 12)}
              </span>
            </div>
          ) : null}
        </div>

        {error ? (
          <Alert status="danger">
            <Alert.Indicator>!</Alert.Indicator>
            <Alert.Content>
              <Alert.Title>{error.code}</Alert.Title>
              <Alert.Description>
                {error.message}
                {error.recoverableSync
                  ? " 同步任务已持久化，恢复 Workflow 后会继续等待。"
                  : ""}
              </Alert.Description>
              <div className="mt-3 flex flex-wrap gap-2">
                {agentLink ? (
                  <Link href={`/tasks/${agentLink}?resume=1`}>
                    <Button variant="primary" size="sm">
                      打开任务并恢复
                    </Button>
                  </Link>
                ) : (
                  <Button variant="primary" size="sm" onPress={onRetry}>
                    {error.recoverableSync ? "恢复同步并继续" : "重试"}
                  </Button>
                )}
                <Button variant="ghost" size="sm" onPress={onReset}>
                  重新输入
                </Button>
              </div>
            </Alert.Content>
          </Alert>
        ) : flow.phase === "done" && agentLink ? (
          <Alert status="success">
            <Alert.Indicator>✓</Alert.Indicator>
            <Alert.Content>
              <Alert.Title>Agent 已创建</Alert.Title>
              <Alert.Description>
                任务已提交，可以打开事件日志查看完整回放和实时输出。
              </Alert.Description>
              <div className="mt-3">
                <Link href={`/tasks/${agentLink}`}>
                  <Button variant="primary" size="sm">
                    打开事件日志 →
                  </Button>
                </Link>
              </div>
            </Alert.Content>
          </Alert>
        ) : (
          <div className="flex items-center gap-2 text-xs text-muted">
            <Spinner size="sm" />
            <span>
              {flow.phase === "syncing"
                ? "正在同步 GitHub 仓库，首次同步可能需要一些时间…"
                : "正在准备分析任务…"}
            </span>
          </div>
        )}
      </Card.Content>
    </Card>
  );
}

export function NewAnalysis() {
  const {
    flow,
    savedFlow,
    hydrated,
    running,
    start,
    retry,
    resumeSaved,
    dismissSaved,
    reset,
  } = useAnalysisFlow();
  const [url, setUrl] = useState("");
  const [task, setTask] = useState(DEFAULT_TASK);
  const [formError, setFormError] = useState<string | null>(null);

  function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setFormError(null);
    const result = start(url, task);
    if (!result.ok) setFormError(result.error);
  }

  return (
    <div className="mx-auto w-full max-w-[1180px] px-4 py-10 sm:px-6 lg:py-14">
      <section className="max-w-3xl">
        <div className="mb-3 flex flex-wrap items-center gap-2">
          <Chip color="accent" variant="soft" size="sm">
            <Chip.Label>READ-ONLY AGENT</Chip.Label>
          </Chip>
          <span className="text-xs text-muted">GitHub → 源码同步 → 只读分析 → 事件流</span>
        </div>
        <h1 className="text-3xl font-semibold tracking-tight sm:text-4xl">
          让 Agent 读代码，
          <span className="text-accent">给出可核对的结论</span>
        </h1>
        <p className="mt-3 max-w-2xl text-sm leading-6 text-muted sm:text-base">
          输入一个公开 GitHub 仓库地址。Sentinel 会同步源码、选择最新就绪版本，
          启动一个只能读取源码的分析 Agent，并把完整事件日志通过 SSE 回放给你。
        </p>
      </section>

      <Card className="mt-8">
        <Card.Content className="p-5 sm:p-6">
          <form onSubmit={submit}>
            <label htmlFor="repository-url" className="text-sm font-medium">
              GitHub 仓库地址
            </label>
            <div className="mt-2 flex flex-col gap-3 sm:flex-row">
              <Input
                id="repository-url"
                aria-label="GitHub 仓库地址"
                placeholder="https://github.com/owner/repository"
                value={url}
                onChange={(event) => setUrl(event.target.value)}
                fullWidth
                autoComplete="off"
                spellCheck={false}
                disabled={running}
                className="h-11 rounded-xl border-separator/80 bg-background/40"
              />
              <Button
                type="submit"
                variant="primary"
                size="lg"
                isDisabled={running}
                className="shrink-0"
              >
                {running ? (
                  <span className="flex items-center gap-2">
                    <Spinner size="sm" color="current" />
                    处理中
                  </span>
                ) : (
                  "开始分析 →"
                )}
              </Button>
            </div>

            <Accordion hideSeparator className="mt-3">
              <Accordion.Item id="advanced">
                <Accordion.Heading>
                  <Accordion.Trigger className="text-xs text-muted">
                    分析要求（可选）
                    <Accordion.Indicator />
                  </Accordion.Trigger>
                </Accordion.Heading>
                <Accordion.Panel>
                  <Accordion.Body>
                    <TextArea
                      aria-label="分析要求"
                      value={task}
                      onChange={(event) => setTask(event.target.value)}
                      fullWidth
                      rows={4}
                      disabled={running}
                      className="rounded-xl border-separator/80 bg-background/40 text-sm"
                    />
                    <p className="mt-2 text-[11px] text-muted">
                      留空或恢复默认：关注架构、核心模块、数据流、风险与源码证据。
                    </p>
                  </Accordion.Body>
                </Accordion.Panel>
              </Accordion.Item>
            </Accordion>

            {formError ? (
              <div className="mt-3">
                <Alert status="danger">
                  <Alert.Indicator>!</Alert.Indicator>
                  <Alert.Content>
                    <Alert.Title>地址不合法</Alert.Title>
                    <Alert.Description>{formError}</Alert.Description>
                  </Alert.Content>
                </Alert>
              </div>
            ) : null}

            <p className="mt-3 text-[11px] leading-5 text-muted">
              当前只支持 GitHub 公开仓库。私有仓库需要在 Source Worker 配置凭据引用，
              网页端暂不提供凭据输入。
            </p>
          </form>
        </Card.Content>
      </Card>

      {hydrated && savedFlow && !running ? (
        <SavedFlowCard
          url={savedFlow.url}
          phase={savedFlow.phase}
          agentId={savedFlow.agentId}
          onResume={resumeSaved}
          onDismiss={dismissSaved}
        />
      ) : null}

      {flow.phase !== "idle" ? (
        <FlowPanel flow={flow} onRetry={retry} onReset={reset} />
      ) : null}

      <RecentTasks />
    </div>
  );
}
