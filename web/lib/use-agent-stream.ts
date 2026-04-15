"use client";

import { useCallback, useEffect, useRef, useState } from "react";

import { TENANT_ID } from "./config";
import type { AgentEvent } from "./types";

export type StreamState =
  | "connecting"
  | "streaming"
  | "reconnecting"
  | "closed"
  | "error";

export class StreamError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "StreamError";
    this.status = status;
    this.code = code;
  }
}

export interface UseAgentStreamOptions {
  /** Fetch the Agent and report whether it is already terminal. Used when the
   * SSE connection closes without a terminal event (e.g. Redis history expired). */
  checkTerminal: () => Promise<boolean>;
  onTerminal?: (event: AgentEvent) => void;
  onEvent?: (event: AgentEvent) => void;
}

export interface UseAgentStream {
  events: AgentEvent[];
  state: StreamState;
  error: StreamError | null;
  terminal: AgentEvent | null;
  lastEventId: string;
  reconnect: () => void;
  stop: () => void;
}

const BACKOFF_MS = [800, 1600, 3200, 6400, 10000];

function backoff(attempt: number): number {
  return BACKOFF_MS[Math.min(attempt, BACKOFF_MS.length - 1)];
}

function abortError(): DOMException {
  return new DOMException("Aborted", "AbortError");
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

function isTerminalType(type: string): boolean {
  return type === "agent.completed" || type === "agent.failed" || type === "agent.cancelled";
}

function parseFrame(frame: string): { id: string; type: string; data: string } | null {
  let id = "";
  let type = "";
  const data: string[] = [];
  for (const rawLine of frame.split("\n")) {
    const line = rawLine.endsWith("\r") ? rawLine.slice(0, -1) : rawLine;
    if (!line || line.startsWith(":")) continue;
    const colon = line.indexOf(":");
    const field = colon === -1 ? line : line.slice(0, colon);
    let value = colon === -1 ? "" : line.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    if (field === "id") id = value;
    else if (field === "event") type = value;
    else if (field === "data") data.push(value);
  }
  if (data.length === 0) return null;
  return { id, type, data: data.join("\n") };
}

export function useAgentStream(
  agentId: string,
  options: UseAgentStreamOptions,
): UseAgentStream {
  const [events, setEvents] = useState<AgentEvent[]>([]);
  const [state, setState] = useState<StreamState>("connecting");
  const [error, setError] = useState<StreamError | null>(null);
  const [terminal, setTerminal] = useState<AgentEvent | null>(null);
  const [lastEventId, setLastEventId] = useState("");

  const callbacksRef = useRef(options);
  useEffect(() => {
    callbacksRef.current = options;
  }, [options]);

  const seenRef = useRef(new Set<string>());
  const lastIdRef = useRef("");
  const bufferRef = useRef<AgentEvent[]>([]);
  const flushTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const abortRef = useRef<AbortController | null>(null);
  const terminalRef = useRef(false);

  const flush = useCallback(() => {
    if (flushTimerRef.current) {
      clearTimeout(flushTimerRef.current);
      flushTimerRef.current = null;
    }
    const batch = bufferRef.current;
    if (batch.length === 0) return;
    bufferRef.current = [];
    setEvents((previous) => [...previous, ...batch]);
    setLastEventId(lastIdRef.current);
  }, []);

  const enqueue = useCallback(
    (event: AgentEvent) => {
      bufferRef.current.push(event);
      if (flushTimerRef.current) return;
      flushTimerRef.current = setTimeout(() => {
        flushTimerRef.current = null;
        flush();
      }, 100);
    },
    [flush],
  );

  const handleTerminal = useCallback(
    (event: AgentEvent) => {
      terminalRef.current = true;
      flush();
      setTerminal(event);
      setState("closed");
      callbacksRef.current.onTerminal?.(event);
      abortRef.current?.abort();
    },
    [flush],
  );

  const connect = useCallback(async () => {
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    terminalRef.current = false;
    setError(null);
    let attempt = 0;

    while (!controller.signal.aborted) {
      setState(lastIdRef.current ? "reconnecting" : "connecting");
      try {
        const params = new URLSearchParams({ tenant_id: TENANT_ID });
        if (lastIdRef.current) {
          // A browser refresh starts with an empty cursor and receives the full
          // replay. A reconnect resumes after the last delivered Stream ID.
          params.set("last_event_id", lastIdRef.current);
        }
        const response = await fetch(
          `/stream/agents/${encodeURIComponent(agentId)}?${params.toString()}`,
          {
            headers: {
              Accept: "text/event-stream",
              "X-Tenant-ID": TENANT_ID,
            },
            cache: "no-store",
            signal: controller.signal,
          },
        );

        if (!response.ok) {
          let body: { code?: string; error?: string } = {};
          try {
            body = (await response.json()) as { code?: string; error?: string };
          } catch {
            // Keep the HTTP status as the only diagnostic.
          }
          const streamError = new StreamError(
            response.status,
            body.code ?? "stream_failed",
            body.error ?? `事件流连接失败（HTTP ${response.status}）`,
          );
          if (
            response.status === 400 ||
            response.status === 404 ||
            response.status === 503
          ) {
            setError(streamError);
            setState("error");
            return;
          }
          throw streamError;
        }

        if (!response.body) {
          throw new StreamError(0, "stream_empty", "事件流没有响应正文");
        }

        attempt = 0;
        setState("streaming");
        const reader = response.body.getReader();
        const decoder = new TextDecoder();
        let pending = "";

        while (!controller.signal.aborted) {
          const { value, done } = await reader.read();
          if (done) break;
          pending += decoder.decode(value, { stream: true });
          let boundary = pending.indexOf("\n\n");
          while (boundary !== -1) {
            const frame = pending.slice(0, boundary);
            pending = pending.slice(boundary + 2);
            const parsed = parseFrame(frame);
            if (parsed) {
              let raw: Record<string, unknown>;
              try {
                raw = JSON.parse(parsed.data) as Record<string, unknown>;
              } catch {
                raw = {};
              }
              const type =
                typeof raw.type === "string" && raw.type
                  ? raw.type
                  : parsed.type;
              if (type) {
                const key =
                  parsed.id ||
                  `${type}-${Date.now()}-${Math.random().toString(36).slice(2)}`;
                if (!seenRef.current.has(key)) {
                  seenRef.current.add(key);
                  const event: AgentEvent = {
                    id: key,
                    type,
                    step_id:
                      typeof raw.step_id === "number" ? raw.step_id : undefined,
                    attempt:
                      typeof raw.attempt === "number" ? raw.attempt : undefined,
                    data: raw.data,
                    created_at:
                      typeof raw.created_at === "string"
                        ? raw.created_at
                        : new Date().toISOString(),
                  };
                  if (parsed.id) lastIdRef.current = parsed.id;
                  enqueue(event);
                  callbacksRef.current.onEvent?.(event);
                  if (isTerminalType(type)) {
                    handleTerminal(event);
                    return;
                  }
                }
              }
            }
            boundary = pending.indexOf("\n\n");
          }
        }

        if (controller.signal.aborted || terminalRef.current) return;

        let agentTerminal = false;
        try {
          agentTerminal = await callbacksRef.current.checkTerminal();
        } catch {
          // If the metadata check fails, keep reconnecting.
        }
        if (controller.signal.aborted) return;
        if (agentTerminal) {
          flush();
          setState("closed");
          return;
        }
        setState("reconnecting");
        await sleep(backoff(attempt++), controller.signal);
      } catch (caught) {
        if (controller.signal.aborted) return;
        if (
          caught instanceof StreamError &&
          (caught.status === 400 ||
            caught.status === 404 ||
            caught.status === 503)
        ) {
          setError(caught);
          setState("error");
          return;
        }
        const streamError =
          caught instanceof StreamError
            ? caught
            : new StreamError(
                0,
                "stream_network_error",
                caught instanceof Error ? caught.message : "事件流网络错误",
              );
        setError(streamError);
        setState("reconnecting");
        try {
          await sleep(backoff(attempt++), controller.signal);
        } catch {
          return;
        }
      }
    }
  }, [agentId, enqueue, flush, handleTerminal]);

  useEffect(() => {
    setEvents([]);
    setTerminal(null);
    setError(null);
    setLastEventId("");
    seenRef.current = new Set();
    lastIdRef.current = "";
    bufferRef.current = [];
    void connect();
    return () => {
      abortRef.current?.abort();
      if (flushTimerRef.current) {
        clearTimeout(flushTimerRef.current);
        flushTimerRef.current = null;
      }
    };
  }, [agentId, connect]);

  const reconnect = useCallback(() => {
    if (terminalRef.current) return;
    void connect();
  }, [connect]);

  const stop = useCallback(() => {
    abortRef.current?.abort();
    flush();
    setState("closed");
  }, [flush]);

  return {
    events,
    state,
    error,
    terminal,
    lastEventId,
    reconnect,
    stop,
  };
}
