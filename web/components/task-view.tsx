"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import Link from "next/link";
import {
  Alert,
  Button,
  Drawer,
  Input,
  Modal,
  Skeleton,
  Spinner,
  Switch,
} from "@heroui/react";
import {
  ArrowDown,
  ArrowLeft,
  CircleAlert,
  CircleCheck,
  Download,
  Link2,
  PanelRight,
  Play,
  RefreshCw,
  XCircle,
} from "lucide-react";

import { api } from "@/lib/api";
import { buildConsole } from "@/lib/console";
import { eventDataString } from "@/lib/events";
import { formatDuration, isAgentTerminal } from "@/lib/format";
import type { AgentView } from "@/lib/types";
import { useAgentStream } from "@/lib/use-agent-stream";
import { AgentMeta } from "./agent-meta";
import { AnalysisReport } from "./analysis-report";
import { ConsoleLog } from "./console-log";
import { StatusChip } from "./status-chip";
import { StreamStatus } from "./stream-status";

type Notice = {
  tone: "success" | "danger" | "accent" | "warning";
  text: string;
};

function TaskSkeleton() {
  return (
    <div className="mx-auto w-full max-w-[1200px] space-y-4 px-4 py-8 sm:px-6">
      <Skeleton className="h-7 w-56 rounded-lg" />
      <Skeleton className="h-12 w-full rounded-xl" />
      <Skeleton className="h-[420px] w-full rounded-2xl" />
    </div>
  );
}

function TaskError({ message }: { message: string }) {
  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col items-start px-4 py-16 sm:px-6">
      <Alert status="danger" className="w-full">
        <Alert.Indicator>
          <CircleAlert size={16} />
        </Alert.Indicator>
        <Alert.Content>
          <Alert.Title>无法打开任务</Alert.Title>
          <Alert.Description>{message}</Alert.Description>
          <div className="mt-4 flex gap-2">
            <Link href="/">
              <Button variant="primary" size="sm">
                返回新建分析
              </Button>
            </Link>
          </div>
        </Alert.Content>
      </Alert>
    </div>
  );
}

export function TaskView({ agentId }: { agentId: string }) {
  const [agent, setAgent] = useState<AgentView | null>(null);
  const [agentLoading, setAgentLoading] = useState(true);
  const [agentError, setAgentError] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [follow, setFollow] = useState(true);
  const [notice, setNotice] = useState<Notice | null>(null);
  const [busy, setBusy] = useState(false);
  const [resumeRequested, setResumeRequested] = useState(false);
  const timelineEndRef = useRef<HTMLDivElement | null>(null);
  const [cancelOpen, setCancelOpen] = useState(false);

  const loadAgent = useCallback(async () => {
    try {
      const next = await api.getAgent(agentId);
      setAgent(next);
      setAgentError(null);
      return next;
    } catch (caught) {
      setAgentError(caught instanceof Error ? caught.message : "加载任务失败");
      return null;
    } finally {
      setAgentLoading(false);
    }
  }, [agentId]);

  const checkTerminal = useCallback(async () => {
    try {
      const next = await api.getAgent(agentId);
      setAgent(next);
      return isAgentTerminal(next.status);
    } catch {
      return false;
    }
  }, [agentId]);

  const handleTerminal = useCallback(() => {
    void loadAgent();
  }, [loadAgent]);

  const stream = useAgentStream(agentId, {
    checkTerminal,
    onTerminal: handleTerminal,
  });

  useEffect(() => {
    void loadAgent();
  }, [loadAgent]);

  useEffect(() => {
    const interval = window.setInterval(() => {
      void loadAgent();
    }, 10000);
    return () => window.clearInterval(interval);
  }, [loadAgent]);

  useEffect(() => {
    setResumeRequested(new URLSearchParams(window.location.search).get("resume") === "1");
  }, [agentId]);

  useEffect(() => {
    if (!notice) return;
    const timer = window.setTimeout(() => setNotice(null), 5000);
    return () => window.clearTimeout(timer);
  }, [notice]);

  useEffect(() => {
    if (!follow) return;
    const frame = window.requestAnimationFrame(() => {
      timelineEndRef.current?.scrollIntoView({ block: "end", behavior: "auto" });
    });
    return () => window.cancelAnimationFrame(frame);
  }, [stream.events.length, follow]);

  useEffect(() => {
    function onScroll() {
      const nearBottom =
        window.innerHeight + window.scrollY >= document.documentElement.scrollHeight - 120;
      setFollow((current) => (current === nearBottom ? current : nearBottom));
    }
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => window.removeEventListener("scroll", onScroll);
  }, []);

  const terminalStatus = useMemo(() => {
    if (!stream.terminal) return undefined;
    if (stream.terminal.type === "agent.completed") return "completed";
    if (stream.terminal.type === "agent.failed") return "failed";
    return "cancelled";
  }, [stream.terminal]);

  const latestEventStatus = useMemo(() => {
    for (let index = stream.events.length - 1; index >= 0; index -= 1) {
      const event = stream.events[index];
      if (event.type === "agent.status") {
        const status = eventDataString(event, "status");
        if (status) return status;
      }
    }
    return undefined;
  }, [stream.events]);

  const displayStatus = terminalStatus ?? latestEventStatus ?? agent?.status ?? "pending";
  const terminal = isAgentTerminal(displayStatus);
  const consoleModel = useMemo(() => buildConsole(stream.events), [stream.events]);
  const queryText = query.trim().toLowerCase();
  const visibleLines = useMemo(() => {
    if (!queryText) return consoleModel.lines;
    return consoleModel.lines.filter((line) =>
      JSON.stringify(line).toLowerCase().includes(queryText),
    );
  }, [consoleModel.lines, queryText]);
  const reportVisible = Boolean(
    consoleModel.report &&
      (!queryText || consoleModel.report.toLowerCase().includes(queryText)),
  );

  const canCancel =
    !terminal &&
    (displayStatus === "running" ||
      displayStatus === "waiting" ||
      displayStatus === "cancelling");
  const canResume =
    !terminal &&
    (resumeRequested || displayStatus === "pending" || stream.state === "error");

  async function copyLink() {
    try {
      await navigator.clipboard.writeText(window.location.href);
      setNotice({ tone: "success", text: "任务链接已复制" });
    } catch {
      setNotice({ tone: "danger", text: "复制失败，请手动复制地址栏链接" });
    }
  }

  async function confirmCancel() {
    setBusy(true);
    setCancelOpen(false);
    try {
      await api.cancelAgent(agentId);
      setNotice({ tone: "accent", text: "取消请求已发送，等待 Workflow 写入终态" });
      await loadAgent();
      stream.reconnect();
    } catch (caught) {
      setNotice({
        tone: "danger",
        text: caught instanceof Error ? caught.message : "取消任务失败",
      });
    } finally {
      setBusy(false);
    }
  }

  async function resume() {
    setBusy(true);
    try {
      await api.resumeAgent(agentId);
      setResumeRequested(false);
      setNotice({ tone: "accent", text: "已请求恢复 Workflow" });
      await loadAgent();
      stream.reconnect();
    } catch (caught) {
      setNotice({
        tone: "danger",
        text: caught instanceof Error ? caught.message : "恢复任务失败",
      });
    } finally {
      setBusy(false);
    }
  }

  function exportEvents() {
    if (stream.events.length === 0) return;
    const payload = stream.events.map((event) => JSON.stringify(event)).join("\n");
    const blob = new Blob([payload], { type: "application/x-ndjson" });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `sentinel-${agentId}.jsonl`;
    document.body.appendChild(anchor);
    anchor.click();
    anchor.remove();
    URL.revokeObjectURL(url);
  }

  function scrollToBottom() {
    setFollow(true);
    timelineEndRef.current?.scrollIntoView({ behavior: "smooth", block: "end" });
  }

  if (agentLoading && !agent) return <TaskSkeleton />;
  if (agentError && !agent) return <TaskError message={agentError} />;
  if (!agent) return <TaskError message="任务不存在或无法访问。" />;

  return (
    <div className="mx-auto w-full max-w-[1500px] px-4 py-6 sm:px-6 lg:py-8">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <Link
            href="/"
            className="inline-flex items-center gap-1 text-xs text-muted hover:text-foreground"
          >
            <ArrowLeft size={14} />
            新建分析
          </Link>
          <div className="mt-1 flex flex-wrap items-center gap-2">
            <h1 className="text-xl font-semibold tracking-tight">分析任务</h1>
            <StatusChip status={displayStatus} size="md" />
          </div>
          <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted">
            <StreamStatus state={stream.state} error={stream.error} />
            <span className="text-separator">·</span>
            <span>创建于 {new Date(agent.created_at).toLocaleString("zh-CN")}</span>
            <span className="text-separator">·</span>
            <span>
              已运行 {formatDuration(agent.started_at ?? agent.created_at, agent.finished_at)}
            </span>
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-1.5">
          <Button
            isIconOnly
            variant="secondary"
            size="sm"
            aria-label="复制链接"
            onPress={copyLink}
          >
            <Link2 size={16} />
          </Button>
          <Button
            isIconOnly
            variant="secondary"
            size="sm"
            aria-label="刷新任务"
            onPress={() => void loadAgent()}
          >
            <RefreshCw size={16} />
          </Button>
          <Drawer>
            <Button
              isIconOnly
              variant="secondary"
              size="sm"
              aria-label="任务详情"
            >
              <PanelRight size={16} />
            </Button>
            <Drawer.Backdrop isDismissable>
              <Drawer.Content placement="right" className="w-[360px] bg-background">
                <Drawer.Dialog>
                  <Drawer.Header>
                    <Drawer.Heading>任务详情</Drawer.Heading>
                  </Drawer.Header>
                  <Drawer.Body className="p-4">
                    <AgentMeta agent={agent} />
                  </Drawer.Body>
                </Drawer.Dialog>
              </Drawer.Content>
            </Drawer.Backdrop>
          </Drawer>
          <Button
            isIconOnly
            variant="secondary"
            size="sm"
            aria-label="导出 JSONL"
            onPress={exportEvents}
            isDisabled={stream.events.length === 0}
          >
            <Download size={16} />
          </Button>
          {canResume ? (
            <Button
              variant="primary"
              size="sm"
              className="gap-1.5"
              onPress={() => void resume()}
              isDisabled={busy}
            >
              {busy ? <Spinner size="sm" color="current" /> : <Play size={14} />}
              恢复 Workflow
            </Button>
          ) : null}
          {canCancel ? (
            <Modal isOpen={cancelOpen} onOpenChange={setCancelOpen}>
              <Button
                variant="danger-soft"
                size="sm"
                className="gap-1.5"
                isDisabled={busy}
              >
                <XCircle size={14} />
                取消任务
              </Button>
              <Modal.Backdrop isDismissable>
                <Modal.Container>
                  <Modal.Dialog>
                    <Modal.Header>
                      <Modal.Heading>取消这个分析任务？</Modal.Heading>
                    </Modal.Header>
                    <Modal.Body>
                      <p className="text-sm text-muted">
                        Temporal Workflow 会收到取消信号，当前步骤完成后停止。已经保存的事件和结果不会删除。
                      </p>
                    </Modal.Body>
                    <Modal.Footer>
                      <Button
                        variant="ghost"
                        size="sm"
                        onPress={() => setCancelOpen(false)}
                      >
                        返回
                      </Button>
                      <Button
                        variant="danger"
                        size="sm"
                        onPress={() => void confirmCancel()}
                        isDisabled={busy}
                      >
                        {busy ? <Spinner size="sm" color="current" /> : "确认取消"}
                      </Button>
                    </Modal.Footer>
                  </Modal.Dialog>
                </Modal.Container>
              </Modal.Backdrop>
            </Modal>
          ) : null}
        </div>
      </div>

      {notice ? (
        <div className="mb-3">
          <Alert status={notice.tone}>
            <Alert.Indicator>
              {notice.tone === "success" ? (
                <CircleCheck size={16} />
              ) : (
                <CircleAlert size={16} />
              )}
            </Alert.Indicator>
            <Alert.Content>
              <Alert.Description>{notice.text}</Alert.Description>
            </Alert.Content>
          </Alert>
        </div>
      ) : null}

      {resumeRequested && !terminal ? (
        <div className="mb-3">
          <Alert status="warning">
            <Alert.Indicator><CircleAlert size={16} /></Alert.Indicator>
            <Alert.Content>
              <Alert.Title>Workflow 尚未恢复</Alert.Title>
              <Alert.Description>
                Agent 已持久化，但 Temporal Workflow 可能没有启动。点击“恢复 Workflow”
                重新调度并继续接收事件。
              </Alert.Description>
            </Alert.Content>
          </Alert>
        </div>
      ) : null}

      {stream.state === "error" ? (
        <div className="mb-3">
          <Alert status="warning">
            <Alert.Indicator><CircleAlert size={16} /></Alert.Indicator>
            <Alert.Content>
              <Alert.Title>
                {stream.error?.code === "streaming_disabled"
                  ? "实时事件不可用"
                  : "事件流连接失败"}
              </Alert.Title>
              <Alert.Description>
                {stream.error?.code === "streaming_disabled"
                  ? "API 未配置 Redis，页面会继续轮询任务状态和最终结果。"
                  : stream.error?.message ?? "正在尝试恢复连接。"}
              </Alert.Description>
              <div className="mt-3">
                <Button variant="secondary" size="sm" onPress={stream.reconnect}>
                  重新连接事件流
                </Button>
              </div>
            </Alert.Content>
          </Alert>
        </div>
      ) : null}

      {agentError ? (
        <div className="mb-3">
          <Alert status="danger">
            <Alert.Indicator><CircleAlert size={16} /></Alert.Indicator>
            <Alert.Content>
              <Alert.Title>任务状态刷新失败</Alert.Title>
              <Alert.Description>{agentError}</Alert.Description>
            </Alert.Content>
          </Alert>
        </div>
      ) : null}

      <section className="min-w-0">
        <div className="mb-3 flex flex-wrap items-center gap-2">
          <span className="mr-1 text-xs font-medium text-muted">执行日志</span>
          <Input
            aria-label="搜索日志"
            placeholder="搜索日志…"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            className="h-9 w-[220px] rounded-xl border-separator/80 bg-background/40 text-xs"
          />

          <Switch
            isSelected={follow}
            onChange={setFollow}
            size="sm"
            aria-label="跟随最新事件"
          >
            <Switch.Content>
              <Switch.Control>
                <Switch.Thumb />
              </Switch.Control>
              <span className="text-[11px] text-muted">跟随最新</span>
            </Switch.Content>
          </Switch>

          {!follow ? (
            <Button variant="ghost" size="sm" className="gap-1.5" onPress={scrollToBottom}>
              <ArrowDown size={14} />
              回到底部
            </Button>
          ) : null}
        </div>

        <ConsoleLog
          lines={visibleLines}
          emptyLabel={
            queryText
              ? "没有匹配的日志"
              : terminal
                ? "没有可显示的执行记录"
                : undefined
          }
        />
        <AnalysisReport markdown={reportVisible ? consoleModel.report : undefined} />
        <div ref={timelineEndRef} aria-hidden="true" className="h-px w-full" />
      </section>
    </div>
  );
}
