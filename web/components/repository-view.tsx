"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import {
  Alert,
  Button,
  Card,
  Input,
  ListBox,
  Select,
  Skeleton,
  TextArea,
} from "@heroui/react";
import {
  ArrowLeft,
  CircleAlert,
  CircleCheck,
  GitBranch,
  Pencil,
  RefreshCw,
  ScrollText,
} from "lucide-react";

import { ApiError, api } from "@/lib/api";
import { formatDateTime, formatRelative, repositoryLabel } from "@/lib/format";
import type { RefRecord, Repository, Snapshot, SyncRecord } from "@/lib/types";
import { StatusChip } from "./status-chip";

function sleep(milliseconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

function parseRefs(value: string): string[] {
  return value
    .split(/[\n,]/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function isTerminal(status: string | undefined): boolean {
  return status === "succeeded" || status === "failed" || status === "cancelled";
}

export function RepositoryView({ repositoryId }: { repositoryId: string }) {
  const [repository, setRepository] = useState<Repository | null>(null);
  const [refs, setRefs] = useState<RefRecord[]>([]);
  const [syncs, setSyncs] = useState<SyncRecord[]>([]);
  const [versions, setVersions] = useState<Snapshot[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [syncing, setSyncing] = useState(false);
  const [syncStatus, setSyncStatus] = useState<string | null>(null);
  const [syncError, setSyncError] = useState<string | null>(null);
  const [editing, setEditing] = useState(false);
  const [trackedRefs, setTrackedRefs] = useState("");
  const [credentialRef, setCredentialRef] = useState("");
  const [status, setStatus] = useState("active");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [nextRepository, refsPage, syncsPage, versionsPage] = await Promise.all([
        api.getRepository(repositoryId),
        api.listRepositoryRefs(repositoryId, 300),
        api.listRepositorySyncs(repositoryId, 50),
        api.listVersions(repositoryId, 50),
      ]);
      setRepository(nextRepository);
      setRefs(refsPage.refs);
      setSyncs(syncsPage.syncs);
      setVersions(versionsPage.versions);
      setTrackedRefs(nextRepository.tracked_refs.join("\n"));
      setCredentialRef(nextRepository.credential_ref ?? "");
      setStatus(nextRepository.status);
      setError(null);
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "加载仓库失败");
    } finally {
      setLoading(false);
    }
  }, [repositoryId]);

  useEffect(() => {
    void load();
  }, [load]);

  async function syncNow() {
    setSyncing(true);
    setSyncError(null);
    setSyncStatus(null);
    try {
      let job;
      try {
        job = await api.createSync(repositoryId);
      } catch (caught) {
        if (
          caught instanceof ApiError &&
          caught.code === "workflow_start_failed" &&
          typeof caught.body.sync_id === "string"
        ) {
          await api.resumeSync(caught.body.sync_id);
          job = await api.getSync(caught.body.sync_id);
        } else {
          throw caught;
        }
      }
      let current = job;
      setSyncStatus(current.status);
      for (let attempt = 0; attempt < 300 && !isTerminal(current.status); attempt += 1) {
        await sleep(2000);
        current = await api.getSync(job.sync_id);
        setSyncStatus(current.status);
      }
      if (current.status === "succeeded") {
        setNotice("源码同步完成");
      } else if (current.status === "failed" || current.status === "cancelled") {
        setSyncError(current.error ? JSON.stringify(current.error) : `同步${current.status === "failed" ? "失败" : "已取消"}`);
      } else {
        setSyncError("同步仍在运行，可稍后刷新查看结果");
      }
      await load();
    } catch (caught) {
      setSyncError(caught instanceof Error ? caught.message : "同步失败");
    } finally {
      setSyncing(false);
    }
  }

  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const refs = parseRefs(trackedRefs);
    if (refs.length === 0) {
      setError("至少保留一个 tracked ref");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const updated = await api.updateRepository(repositoryId, {
        tracked_refs: refs,
        credential_ref: credentialRef.trim(),
        status,
      });
      setRepository(updated);
      setEditing(false);
      setNotice("仓库配置已更新");
      await load();
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "更新仓库失败");
    } finally {
      setBusy(false);
    }
  }

  if (loading && !repository) {
    return (
      <div className="mx-auto w-full max-w-[1180px] space-y-4 px-4 py-8 sm:px-6">
        <Skeleton className="h-8 w-72 rounded-lg" />
        <Skeleton className="h-32 w-full rounded-xl" />
        <Skeleton className="h-64 w-full rounded-xl" />
      </div>
    );
  }

  if (!repository) {
    return (
      <div className="mx-auto w-full max-w-3xl px-4 py-16 sm:px-6">
        <Alert status="danger">
          <Alert.Indicator>
            <CircleAlert size={16} />
          </Alert.Indicator>
          <Alert.Content>
            <Alert.Title>无法打开仓库</Alert.Title>
            <Alert.Description>{error ?? "仓库不存在或无法访问。"}</Alert.Description>
            <div className="mt-4">
              <Link href="/repositories">
                <Button variant="secondary" size="sm">
                  返回仓库列表
                </Button>
              </Link>
            </div>
          </Alert.Content>
        </Alert>
      </div>
    );
  }

  return (
    <div className="mx-auto w-full max-w-[1180px] px-4 py-8 sm:px-6 lg:py-10">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <Link href="/repositories" className="inline-flex items-center gap-1 text-xs text-muted hover:text-foreground">
            <ArrowLeft size={14} />
            仓库
          </Link>
          <div className="mt-1 flex flex-wrap items-center gap-2">
            <h1 className="text-2xl font-semibold tracking-tight">
              {repositoryLabel(repository.remote_url)}
            </h1>
            <StatusChip status={repository.status} />
          </div>
          <p className="mt-1 break-all font-mono text-[11px] text-muted">{repository.remote_url}</p>
          <div className="mt-2 flex flex-wrap gap-3 text-[11px] text-muted">
            <span>{repository.tracked_refs.length} 个跟踪 ref</span>
            <span>{repository.object_format}</span>
            <span>{repository.archive_version ? `归档 v${repository.archive_version}` : "尚未同步"}</span>
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button
            variant="secondary"
            size="sm"
            className="gap-1.5"
            onPress={() => setEditing((value) => !value)}
          >
            <Pencil size={14} />
            编辑
          </Button>
          <Button
            variant="primary"
            size="sm"
            className="gap-1.5"
            isDisabled={syncing || repository.status !== "active"}
            onPress={() => void syncNow()}
          >
            <RefreshCw size={14} className={syncing ? "animate-spin" : ""} />
            {syncing ? "同步中…" : "立即同步"}
          </Button>
        </div>
      </div>

      {notice ? (
        <Alert status="success" className="mt-4">
          <Alert.Indicator>
            <CircleCheck size={16} />
          </Alert.Indicator>
          <Alert.Content>
            <Alert.Description>{notice}</Alert.Description>
          </Alert.Content>
        </Alert>
      ) : null}
      {error ? (
        <Alert status="danger" className="mt-3">
          <Alert.Indicator>
            <CircleAlert size={16} />
          </Alert.Indicator>
          <Alert.Content>
            <Alert.Description>{error}</Alert.Description>
          </Alert.Content>
        </Alert>
      ) : null}
      {syncStatus ? (
        <Alert status={syncError ? "danger" : isTerminal(syncStatus) ? "success" : "accent"} className="mt-3">
          <Alert.Indicator>
            {syncError ? <CircleAlert size={16} /> : <RefreshCw size={16} className={syncing ? "animate-spin" : ""} />}
          </Alert.Indicator>
          <Alert.Content>
            <Alert.Title>同步状态：{syncStatus}</Alert.Title>
            {syncError ? <Alert.Description>{syncError}</Alert.Description> : null}
          </Alert.Content>
        </Alert>
      ) : null}

      {editing ? (
        <Card className="mt-5 border border-accent/30">
          <Card.Content className="p-4 sm:p-5">
            <form onSubmit={save} className="grid gap-3 md:grid-cols-2">
              <TextArea
                aria-label="跟踪的 refs"
                value={trackedRefs}
                onChange={(event) => setTrackedRefs(event.target.value)}
                rows={5}
                fullWidth
                disabled={busy}
                className="rounded-xl border-separator/80 bg-background/40 font-mono text-xs"
              />
              <div className="space-y-3">
                <Input
                  aria-label="凭据引用"
                  placeholder="凭据引用，例如 env:GITHUB_TOKEN"
                  value={credentialRef}
                  onChange={(event) => setCredentialRef(event.target.value)}
                  disabled={busy}
                  className="w-full rounded-xl border-separator/80 bg-background/40 text-sm"
                />
                <Select
                  aria-label="仓库状态"
                  selectedKey={status}
                  onSelectionChange={(key) => setStatus(String(key ?? "active"))}
                  isDisabled={busy}
                >
                  <Select.Trigger>
                    <Select.Value />
                    <Select.Indicator />
                  </Select.Trigger>
                  <Select.Popover>
                    <ListBox>
                      <ListBox.Item id="active">活跃</ListBox.Item>
                      <ListBox.Item id="paused">已暂停</ListBox.Item>
                      <ListBox.Item id="archived">已归档</ListBox.Item>
                    </ListBox>
                  </Select.Popover>
                </Select>
                <p className="text-[11px] text-muted">每行一个 ref，也可以使用逗号分隔。凭据只保存环境变量引用。</p>
              </div>
              <div className="flex gap-2 md:col-span-2">
                <Button type="submit" variant="primary" size="sm" isDisabled={busy}>
                  保存配置
                </Button>
                <Button variant="ghost" size="sm" onPress={() => setEditing(false)}>
                  取消
                </Button>
              </div>
            </form>
          </Card.Content>
        </Card>
      ) : null}

      <div className="mt-6 grid gap-5 xl:grid-cols-2">
        <Card className="border border-separator/60">
          <Card.Header className="flex-row items-center justify-between gap-2">
            <Card.Title className="flex items-center gap-1.5 text-sm">
              <GitBranch size={15} />
              远端 refs
            </Card.Title>
            <span className="text-[11px] text-muted">最近一次同步观测</span>
          </Card.Header>
          <Card.Content className="p-0">
            {refs.length === 0 ? (
              <p className="p-4 text-sm text-muted">还没有 ref 观测记录，请先执行同步。</p>
            ) : (
              <div className="max-h-[380px] overflow-auto">
                <table className="w-full text-left text-xs">
                  <thead className="sticky top-0 bg-surface text-[10px] uppercase tracking-wide text-muted">
                    <tr>
                      <th className="px-4 py-2 font-medium">ref</th>
                      <th className="px-4 py-2 font-medium">commit</th>
                      <th className="px-4 py-2 font-medium">快照</th>
                      <th className="px-4 py-2 font-medium">观测</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-separator/40">
                    {refs.map((ref) => (
                      <tr key={ref.ref_name}>
                        <td className="max-w-[260px] break-all px-4 py-2 font-mono text-[10px]">{ref.ref_name}</td>
                        <td className="px-4 py-2 font-mono text-[10px]">{ref.commit_sha?.slice(0, 12) ?? "—"}</td>
                        <td className="px-4 py-2">
                          {ref.snapshot_status ? (
                            <span className={ref.snapshot_status === "ready" ? "text-success" : "text-muted"}>
                              {ref.snapshot_status}
                            </span>
                          ) : (
                            <span className="text-muted">—</span>
                          )}
                        </td>
                        <td className="px-4 py-2 text-[10px] text-muted">{formatRelative(ref.observed_at)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Card.Content>
        </Card>

        <Card className="border border-separator/60">
          <Card.Header className="flex-row items-center justify-between gap-2">
            <Card.Title className="flex items-center gap-1.5 text-sm">
              <ScrollText size={15} />
              同步历史
            </Card.Title>
            <span className="text-[11px] text-muted">最近 50 次</span>
          </Card.Header>
          <Card.Content className="p-0">
            {syncs.length === 0 ? (
              <p className="p-4 text-sm text-muted">还没有同步记录。</p>
            ) : (
              <div className="max-h-[380px] overflow-auto">
                <table className="w-full text-left text-xs">
                  <thead className="sticky top-0 bg-surface text-[10px] uppercase tracking-wide text-muted">
                    <tr>
                      <th className="px-4 py-2 font-medium">状态</th>
                      <th className="px-4 py-2 font-medium">触发</th>
                      <th className="px-4 py-2 font-medium">创建</th>
                      <th className="px-4 py-2 font-medium">结束</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-separator/40">
                    {syncs.map((sync) => (
                      <tr key={sync.sync_id}>
                        <td className="px-4 py-2"><StatusChip status={sync.status} /></td>
                        <td className="px-4 py-2 text-[10px] text-muted">{sync.trigger_type}</td>
                        <td className="px-4 py-2 text-[10px] text-muted">{formatDateTime(sync.created_at)}</td>
                        <td className="px-4 py-2 text-[10px] text-muted">
                          {sync.finished_at ? formatDateTime(sync.finished_at) : "—"}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Card.Content>
        </Card>
      </div>

      <Card className="mt-5 border border-separator/60">
        <Card.Header className="flex-row items-center justify-between gap-2">
          <Card.Title className="text-sm">最近快照</Card.Title>
          <Link href={`/new?repository=${repositoryId}`} className="text-[11px] text-muted hover:text-foreground">
            创建分析
          </Link>
        </Card.Header>
        <Card.Content className="p-0">
          {versions.length === 0 ? (
            <p className="p-4 text-sm text-muted">还没有就绪快照。</p>
          ) : (
            <div className="max-h-[320px] overflow-auto">
              <table className="w-full text-left text-xs">
                <thead className="sticky top-0 bg-surface text-[10px] uppercase tracking-wide text-muted">
                  <tr>
                    <th className="px-4 py-2 font-medium">commit</th>
                    <th className="px-4 py-2 font-medium">tree</th>
                    <th className="px-4 py-2 font-medium">发布</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-separator/40">
                  {versions.map((snapshot) => (
                    <tr key={snapshot.source_snapshot_id}>
                      <td className="px-4 py-2 font-mono text-[10px]">{snapshot.commit_sha.slice(0, 12)}</td>
                      <td className="px-4 py-2 font-mono text-[10px] text-muted">{snapshot.tree_sha.slice(0, 12)}</td>
                      <td className="px-4 py-2 text-[10px] text-muted">{formatDateTime(snapshot.created_at)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card.Content>
      </Card>
    </div>
  );
}
