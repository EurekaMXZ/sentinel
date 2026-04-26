"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { Alert, Button, Card, Input, Skeleton } from "@heroui/react";
import { ArrowRight, CircleAlert, GitBranch, Plus, RefreshCw } from "lucide-react";

import { api } from "@/lib/api";
import { normalizeGitHubUrl } from "@/lib/config";
import { repositoryLabel } from "@/lib/format";
import type { Repository } from "@/lib/types";
import { StatusChip } from "./status-chip";

function parseRefs(value: string): string[] {
  return value
    .split(/[\n,]/)
    .map((item) => item.trim())
    .filter(Boolean);
}

export function RepositoriesView() {
  const [repositories, setRepositories] = useState<Repository[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [url, setUrl] = useState("");
  const [refs, setRefs] = useState("");
  const [creating, setCreating] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);

  async function load() {
    setLoading(true);
    try {
      const page = await api.listRepositories(200);
      setRepositories(page.repositories);
      setError(null);
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "加载仓库失败");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void load();
  }, []);

  async function create(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setCreating(true);
    setError(null);
    setNotice(null);
    try {
      const normalized = normalizeGitHubUrl(url);
      const tracked = parseRefs(refs);
      const repository = await api.createRepository({
        url: normalized,
        ...(tracked.length > 0 ? { tracked_refs: tracked } : {}),
      });
      setRepositories((current) => [repository, ...(current ?? [])]);
      setUrl("");
      setRefs("");
      setNotice(`仓库「${repositoryLabel(repository.remote_url)}」已注册，下一步是执行同步。`);
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "注册仓库失败");
    } finally {
      setCreating(false);
    }
  }

  return (
    <div className="mx-auto w-full max-w-[1180px] px-4 py-8 sm:px-6 lg:py-10">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <p className="flex items-center gap-1.5 text-xs text-muted">
            <GitBranch size={13} />
            源码资源
          </p>
          <h1 className="mt-1 text-2xl font-semibold tracking-tight">仓库</h1>
          <p className="mt-1 max-w-2xl text-sm text-muted">
            仓库在租户内注册一次，可以被多个项目复用。同步会抓取跟踪的 refs，并为可达 commit 发布不可变快照。
          </p>
        </div>
        <Button variant="secondary" size="sm" className="gap-1.5" onPress={() => void load()}>
          <RefreshCw size={14} />
          刷新
        </Button>
      </div>

      <Card className="mt-6 border border-separator/60">
        <Card.Content className="p-4 sm:p-5">
          <form onSubmit={create} className="grid gap-3 md:grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)_auto] md:items-end">
            <Input
              aria-label="GitHub 仓库地址"
              placeholder="https://github.com/owner/repository"
              value={url}
              onChange={(event) => setUrl(event.target.value)}
              disabled={creating}
              className="rounded-xl border-separator/80 bg-background/40 text-sm"
            />
            <Input
              aria-label="跟踪的 refs"
              placeholder="跟踪 refs（可选，逗号分隔）"
              value={refs}
              onChange={(event) => setRefs(event.target.value)}
              disabled={creating}
              className="rounded-xl border-separator/80 bg-background/40 text-sm"
            />
            <Button
              type="submit"
              variant="primary"
              className="gap-1.5"
              isDisabled={creating || !url.trim()}
            >
              <Plus size={15} />
              {creating ? "注册中…" : "注册仓库"}
            </Button>
          </form>
          <p className="mt-2 text-[11px] text-muted">
            默认跟踪所有分支和标签。留空即可；私有仓库凭据在仓库详情中配置。
          </p>
          {notice ? <p className="mt-2 text-[11px] text-success">{notice}</p> : null}
        </Card.Content>
      </Card>

      {error ? (
        <Alert status="danger" className="mt-4">
          <Alert.Indicator>
            <CircleAlert size={16} />
          </Alert.Indicator>
          <Alert.Content>
            <Alert.Description>{error}</Alert.Description>
          </Alert.Content>
        </Alert>
      ) : null}

      <div className="mt-6">
        {loading && repositories === null ? (
          <div className="grid gap-3 md:grid-cols-2">
            {[0, 1, 2, 3].map((item) => (
              <Skeleton key={item} className="h-32 rounded-xl" />
            ))}
          </div>
        ) : repositories && repositories.length > 0 ? (
          <div className="grid gap-3 md:grid-cols-2">
            {repositories.map((repository) => (
              <Link key={repository.repository_id} href={`/repositories/${repository.repository_id}`}>
                <Card className="h-full border border-separator/60 transition-colors hover:border-accent/50">
                  <Card.Content className="flex h-full flex-col p-4">
                    <div className="flex items-start justify-between gap-3">
                      <div className="min-w-0">
                        <p className="truncate text-sm font-medium">
                          {repositoryLabel(repository.remote_url)}
                        </p>
                        <p className="mt-1 truncate font-mono text-[10px] text-muted">
                          {repository.remote_url}
                        </p>
                      </div>
                      <StatusChip status={repository.status} />
                    </div>
                    <div className="mt-auto flex items-center justify-between gap-3 pt-4 text-[11px] text-muted">
                      <span>
                        {repository.tracked_refs.length} 个跟踪 ref · {repository.object_format}
                      </span>
                      <span className="flex items-center gap-1">
                        {repository.archive_version ? `归档 v${repository.archive_version}` : "尚未同步"}
                        <ArrowRight size={12} />
                      </span>
                    </div>
                  </Card.Content>
                </Card>
              </Link>
            ))}
          </div>
        ) : (
          <Card className="border border-dashed border-separator/70">
            <Card.Content className="p-6 text-sm text-muted">
              还没有注册仓库。在上方输入一个 GitHub HTTPS 地址开始。
            </Card.Content>
          </Card>
        )}
      </div>
    </div>
  );
}
