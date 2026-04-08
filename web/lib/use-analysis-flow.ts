"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";

import { api, ApiError } from "./api";
import { DEFAULT_TASK, normalizeGitHubUrl } from "./config";
import { addRecent, clearFlow, loadFlow, saveFlow } from "./storage";
import type { AnalysisFlow, FlowError, FlowPhase, SyncView } from "./types";

class FlowFailure extends Error {
  readonly code: string;
  readonly step: FlowPhase;
  readonly syncId?: string;
  readonly recoverableSync?: boolean;

  constructor(
    code: string,
    message: string,
    step: FlowPhase,
    extra: { syncId?: string; recoverableSync?: boolean } = {},
  ) {
    super(message);
    this.name = "FlowFailure";
    this.code = code;
    this.step = step;
    this.syncId = extra.syncId;
    this.recoverableSync = extra.recoverableSync;
  }
}

function now(): string {
  return new Date().toISOString();
}

function abortError(): DOMException {
  return new DOMException("Aborted", "AbortError");
}

function throwIfAborted(signal: AbortSignal): void {
  if (signal.aborted) throw abortError();
}

function sleep(milliseconds: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) {
      reject(abortError());
      return;
    }
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", onAbort);
      resolve();
    }, milliseconds);
    const onAbort = () => {
      clearTimeout(timer);
      reject(abortError());
    };
    signal.addEventListener("abort", onAbort, { once: true });
  });
}

function toFlowError(error: unknown, step: FlowPhase, syncId?: string): FlowError {
  if (error instanceof FlowFailure) {
    return {
      code: error.code,
      message: error.message,
      step: error.step,
      syncId: error.syncId ?? syncId,
      recoverableSync: error.recoverableSync,
    };
  }
  if (error instanceof ApiError) {
    return {
      code: error.code,
      message: error.message,
      step,
      syncId: typeof error.body.sync_id === "string" ? error.body.sync_id : syncId,
      agentId: typeof error.body.agent_id === "string" ? error.body.agent_id : undefined,
      recoverableSync: error.code === "workflow_start_failed" && typeof error.body.sync_id === "string",
    };
  }
  if (error instanceof Error) {
    return { code: "unexpected_error", message: error.message, step, syncId };
  }
  return { code: "unexpected_error", message: "发生未知错误", step, syncId };
}

async function pollSync(
  syncId: string,
  signal: AbortSignal,
  onStatus: (status: string) => void,
): Promise<SyncView> {
  for (;;) {
    throwIfAborted(signal);
    const sync = await api.getSync(syncId);
    throwIfAborted(signal);
    onStatus(sync.status);
    if (sync.status === "succeeded") return sync;
    if (sync.status === "failed" || sync.status === "cancelled") {
      const detail = sync.error ? `：${JSON.stringify(sync.error)}` : "";
      throw new FlowFailure(
        `sync_${sync.status}`,
        `源码同步${sync.status === "failed" ? "失败" : "已取消"}${detail}`,
        "syncing",
        { syncId },
      );
    }
    await sleep(2000, signal);
  }
}

const INITIAL_FLOW: AnalysisFlow = {
  phase: "idle",
  url: "",
  task: "",
  updatedAt: new Date(0).toISOString(),
};

export interface UseAnalysisFlow {
  flow: AnalysisFlow;
  savedFlow: AnalysisFlow | null;
  hydrated: boolean;
  running: boolean;
  start: (url: string, task: string) => { ok: true } | { ok: false; error: string };
  retry: () => void;
  resumeSaved: () => void;
  dismissSaved: () => void;
  reset: () => void;
}

export function useAnalysisFlow(): UseAnalysisFlow {
  const router = useRouter();
  const [flow, setFlow] = useState<AnalysisFlow>(INITIAL_FLOW);
  const [savedFlow, setSavedFlow] = useState<AnalysisFlow | null>(null);
  const [hydrated, setHydrated] = useState(false);
  const flowRef = useRef<AnalysisFlow>(INITIAL_FLOW);
  const controllerRef = useRef<AbortController | null>(null);

  const update = useCallback((next: AnalysisFlow) => {
    flowRef.current = next;
    setFlow(next);
    saveFlow(next);
  }, []);

  const patch = useCallback(
    (changes: Partial<AnalysisFlow>) => {
      update({ ...flowRef.current, ...changes, updatedAt: now() });
    },
    [update],
  );

  useEffect(() => {
    setHydrated(true);
    const saved = loadFlow();
    if (saved && saved.phase !== "done" && saved.phase !== "idle" && (saved.syncId || saved.agentId)) {
      setSavedFlow(saved);
    }
    return () => {
      controllerRef.current?.abort();
    };
  }, []);

  const execute = useCallback(
    async (
      base: AnalysisFlow,
      options: { repositoryId?: string; syncId?: string; resumeSync?: boolean } = {},
    ) => {
      controllerRef.current?.abort();
      const controller = new AbortController();
      controllerRef.current = controller;
      const active = () => !controller.signal.aborted;

      if (base.agentId) {
        router.push(`/tasks/${base.agentId}`);
        return;
      }

      let current: AnalysisFlow = {
        ...base,
        phase: "registering",
        error: undefined,
        syncStatus: undefined,
        updatedAt: now(),
      };
      update(current);

      try {
        let repositoryId = options.repositoryId ?? base.repositoryId;
        let syncId = options.syncId ?? base.syncId;

        if (!repositoryId) {
          const normalized = normalizeGitHubUrl(base.url);
          const repositories = await api.listRepositories(200);
          throwIfAborted(controller.signal);
          repositoryId = repositories.repositories.find(
            (repository) => repository.remote_url === normalized,
          )?.repository_id;
          if (!repositoryId) {
            const created = await api.createRepository({ url: normalized });
            throwIfAborted(controller.signal);
            repositoryId = created.repository_id;
          }
        }
        current = { ...current, repositoryId, phase: "syncing" };
        update(current);

        if (options.resumeSync && syncId) {
          await api.resumeSync(syncId);
          throwIfAborted(controller.signal);
        }

        if (!syncId) {
          try {
            const sync = await api.createSync(repositoryId);
            throwIfAborted(controller.signal);
            syncId = sync.sync_id;
          } catch (error) {
            if (
              error instanceof ApiError &&
              error.code === "workflow_start_failed" &&
              typeof error.body.sync_id === "string"
            ) {
              syncId = error.body.sync_id;
              current = {
                ...current,
                syncId,
                phase: "error",
                error: {
                  code: error.code,
                  message:
                    "同步任务已保存，但 Temporal Workflow 未启动。可以恢复该同步后继续。",
                  step: "syncing",
                  syncId,
                  recoverableSync: true,
                },
              };
              update(current);
              return;
            }
            throw error;
          }
        }
        current = { ...current, syncId, phase: "syncing", syncStatus: "queued" };
        update(current);

        const sync = await pollSync(syncId, controller.signal, (status) => {
          if (active()) patch({ syncStatus: status });
        });
        current = { ...current, syncStatus: sync.status, phase: "selecting" };
        update(current);

        const page = await api.listVersions(repositoryId, 1);
        throwIfAborted(controller.signal);
        const snapshot = page.versions?.[0];
        if (!snapshot) {
          throw new FlowFailure(
            "no_ready_snapshot",
            "源码同步已完成，但没有找到就绪版本，请稍后重试。",
            "selecting",
          );
        }
        current = { ...current, snapshot, phase: "creating" };
        update(current);

        let agentId: string;
        try {
          const agent = await api.createAgent({
            task: base.task || DEFAULT_TASK,
            resources: [
              {
                binding_name: "source",
                resource_kind: "source_snapshot",
                repository_id: repositoryId,
                source_snapshot_id: snapshot.source_snapshot_id,
              },
            ],
          });
          agentId = agent.agent_id;
        } catch (error) {
          if (
            error instanceof ApiError &&
            error.code === "workflow_start_failed" &&
            typeof error.body.agent_id === "string"
          ) {
            agentId = error.body.agent_id;
            addRecent({
              agentId,
              url: base.url,
              task: base.task || DEFAULT_TASK,
              createdAt: now(),
            });
            current = { ...current, agentId, phase: "done" };
            update(current);
            router.push(`/tasks/${agentId}?resume=1`);
            return;
          }
          throw error;
        }
        throwIfAborted(controller.signal);

        addRecent({
          agentId,
          url: base.url,
          task: base.task || DEFAULT_TASK,
          createdAt: now(),
        });
        current = { ...current, agentId, phase: "done" };
        update(current);
        router.push(`/tasks/${agentId}`);
      } catch (error) {
        if (!active() || (error instanceof DOMException && error.name === "AbortError")) {
          return;
        }
        const failure = toFlowError(error, current.phase, current.syncId);
        update({ ...current, phase: "error", error: failure, updatedAt: now() });
      }
    },
    [patch, router, update],
  );

  const start = useCallback(
    (url: string, task: string) => {
      try {
        normalizeGitHubUrl(url);
      } catch (error) {
        return {
          ok: false as const,
          error: error instanceof Error ? error.message : "仓库地址不合法",
        };
      }
      setSavedFlow(null);
      clearFlow();
      void execute({
        phase: "idle",
        url: url.trim(),
        task: task.trim() || DEFAULT_TASK,
        updatedAt: now(),
      });
      return { ok: true as const };
    },
    [execute],
  );

  const retry = useCallback(() => {
    const current = flowRef.current;
    if (current.error?.agentId) {
      router.push(`/tasks/${current.error.agentId}?resume=1`);
      return;
    }
    if (!current.url) return;
    const syncTerminal =
      current.syncStatus === "failed" || current.syncStatus === "cancelled";
    const nextSyncId = syncTerminal ? undefined : current.syncId;
    void execute(
      { ...current, phase: "idle", syncId: nextSyncId, error: undefined },
      {
        repositoryId: current.repositoryId,
        syncId: nextSyncId,
        resumeSync: current.error?.recoverableSync === true,
      },
    );
  }, [execute, router]);

  const resumeSaved = useCallback(() => {
    if (!savedFlow) return;
    setSavedFlow(null);
    void execute(savedFlow, {
      repositoryId: savedFlow.repositoryId,
      syncId: savedFlow.syncId,
      resumeSync: savedFlow.error?.recoverableSync === true,
    });
  }, [execute, savedFlow]);

  const dismissSaved = useCallback(() => {
    setSavedFlow(null);
    clearFlow();
  }, []);

  const reset = useCallback(() => {
    controllerRef.current?.abort();
    setSavedFlow(null);
    clearFlow();
    update({ ...INITIAL_FLOW, updatedAt: now() });
  }, [update]);

  const running =
    flow.phase === "registering" ||
    flow.phase === "syncing" ||
    flow.phase === "selecting" ||
    flow.phase === "creating";

  return {
    flow,
    savedFlow,
    hydrated,
    running,
    start,
    retry,
    resumeSaved,
    dismissSaved,
    reset,
  };
}
