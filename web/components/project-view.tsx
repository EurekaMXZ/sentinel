"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import {
  Alert,
  Button,
  Card,
  Chip,
  Input,
  ListBox,
  Select,
  Skeleton,
  Switch,
  TextArea,
} from "@heroui/react";
import {
  ArrowLeft,
  ArrowRight,
  CircleAlert,
  CircleCheck,
  GitBranch,
  Link2,
  Pencil,
  Plus,
  RefreshCw,
  X,
} from "lucide-react";

import { api } from "@/lib/api";
import { formatDateTime, formatRelative, repositoryLabel } from "@/lib/format";
import type { Analysis, Project, Repository } from "@/lib/types";
import { CopyButton } from "./copy-button";
import { StatusChip } from "./status-chip";

function analysisTargetLabel(analysis: Analysis): string {
  const target = analysis.target;
  if (!target) return "未指定目标";
  if (target.kind === "pull_request" && target.pull_request_number) {
    return `PR #${target.pull_request_number}`;
  }
  if (target.commit_sha) return target.commit_sha.slice(0, 12);
  if (target.ref_name) return target.ref_name;
  return target.kind;
}

export function ProjectView({ projectId }: { projectId: string }) {
  const [project, setProject] = useState<Project | null>(null);
  const [analyses, setAnalyses] = useState<Analysis[] | null>(null);
  const [allRepositories, setAllRepositories] = useState<Repository[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [selectedRepository, setSelectedRepository] = useState("");
  const [primary, setPrimary] = useState(false);
  const [editing, setEditing] = useState(false);
  const [editName, setEditName] = useState("");
  const [editDescription, setEditDescription] = useState("");
  const [editStatus, setEditStatus] = useState("active");

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [nextProject, nextAnalyses, repositories] = await Promise.all([
        api.getProject(projectId),
        api.listProjectAnalyses(projectId, 100),
        api.listRepositories(200),
      ]);
      setProject(nextProject);
      setAnalyses(nextAnalyses.analyses);
      setAllRepositories(repositories.repositories);
      setEditName(nextProject.name);
      setEditDescription(nextProject.description);
      setEditStatus(nextProject.status);
      setError(null);
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "加载项目失败");
    } finally {
      setLoading(false);
    }
  }, [projectId]);

  useEffect(() => {
    void load();
  }, [load]);

  const unbound = useMemo(() => {
    const bound = new Set((project?.repositories ?? []).map((repository) => repository.repository_id));
    return allRepositories.filter(
      (repository) => repository.status === "active" && !bound.has(repository.repository_id),
    );
  }, [allRepositories, project]);

  useEffect(() => {
    if (!selectedRepository && unbound.length > 0) {
      setSelectedRepository(unbound[0].repository_id);
    }
  }, [selectedRepository, unbound]);

  async function bindRepository() {
    if (!project || !selectedRepository) return;
    setBusy(true);
    setError(null);
    try {
      await api.addProjectRepository(project.project_id, {
        repository_id: selectedRepository,
        is_primary: primary,
      });
      setNotice("仓库已绑定");
      setSelectedRepository("");
      setPrimary(false);
      await load();
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "绑定仓库失败");
    } finally {
      setBusy(false);
    }
  }

  async function unbindRepository(repositoryId: string) {
    if (!project) return;
    setBusy(true);
    setError(null);
    try {
      await api.removeProjectRepository(project.project_id, repositoryId);
      setNotice("仓库已解绑，历史分析仍会保留");
      await load();
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "解绑仓库失败");
    } finally {
      setBusy(false);
    }
  }

  async function saveProject(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!project) return;
    setBusy(true);
    setError(null);
    try {
      const updated = await api.updateProject(project.project_id, {
        name: editName.trim(),
        description: editDescription,
        status: editStatus,
      });
      setProject(updated);
      setEditing(false);
      setNotice("项目信息已更新");
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "更新项目失败");
    } finally {
      setBusy(false);
    }
  }

  if (loading && !project) {
    return (
      <div className="mx-auto w-full max-w-[1180px] space-y-4 px-4 py-8 sm:px-6">
        <Skeleton className="h-8 w-64 rounded-lg" />
        <Skeleton className="h-40 w-full rounded-xl" />
        <Skeleton className="h-56 w-full rounded-xl" />
      </div>
    );
  }

  if (!project) {
    return (
      <div className="mx-auto w-full max-w-3xl px-4 py-16 sm:px-6">
        <Alert status="danger">
          <Alert.Indicator>
            <CircleAlert size={16} />
          </Alert.Indicator>
          <Alert.Content>
            <Alert.Title>无法打开项目</Alert.Title>
            <Alert.Description>{error ?? "项目不存在或无法访问。"}</Alert.Description>
            <div className="mt-4">
              <Link href="/projects">
                <Button variant="secondary" size="sm">
                  返回项目列表
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
      <div className="mb-4 flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <Link href="/projects" className="inline-flex items-center gap-1 text-xs text-muted hover:text-foreground">
            <ArrowLeft size={14} />
            项目
          </Link>
          <div className="mt-1 flex flex-wrap items-center gap-2">
            <h1 className="text-2xl font-semibold tracking-tight">{project.name}</h1>
            <StatusChip status={project.status} />
          </div>
          <p className="mt-1 max-w-3xl text-sm text-muted">
            {project.description || "暂无项目描述"}
          </p>
          <div className="mt-2 flex flex-wrap items-center gap-3 text-[11px] text-muted">
            <span>{project.repository_count} 个仓库</span>
            <span>{project.analysis_count} 次分析</span>
            <span>创建于 {formatDateTime(project.created_at)}</span>
            <CopyButton value={project.project_id} className="shrink-0" />
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button variant="secondary" size="sm" className="gap-1.5" onPress={() => void load()}>
            <RefreshCw size={14} />
            刷新
          </Button>
          <Button
            variant="secondary"
            size="sm"
            className="gap-1.5"
            onPress={() => setEditing((value) => !value)}
          >
            <Pencil size={14} />
            编辑
          </Button>
          <Link href={`/new?project=${project.project_id}`}>
            <Button variant="primary" size="sm" className="gap-1.5" isDisabled={project.status !== "active"}>
              <Plus size={14} />
              新建分析
            </Button>
          </Link>
        </div>
      </div>

      {notice ? (
        <Alert status="success" className="mb-3">
          <Alert.Indicator>
            <CircleCheck size={16} />
          </Alert.Indicator>
          <Alert.Content>
            <Alert.Description>{notice}</Alert.Description>
          </Alert.Content>
        </Alert>
      ) : null}
      {error ? (
        <Alert status="danger" className="mb-3">
          <Alert.Indicator>
            <CircleAlert size={16} />
          </Alert.Indicator>
          <Alert.Content>
            <Alert.Description>{error}</Alert.Description>
          </Alert.Content>
        </Alert>
      ) : null}

      {editing ? (
        <Card className="mb-5 border border-accent/30">
          <Card.Content className="p-4 sm:p-5">
            <form onSubmit={saveProject} className="grid gap-3 md:grid-cols-2">
              <Input
                aria-label="项目名称"
                value={editName}
                onChange={(event) => setEditName(event.target.value)}
                maxLength={200}
                disabled={busy}
                className="rounded-xl border-separator/80 bg-background/40 text-sm"
              />
              <Select
                aria-label="项目状态"
                selectedKey={editStatus}
                onSelectionChange={(key) => setEditStatus(String(key ?? "active"))}
                isDisabled={busy}
              >
                <Select.Trigger>
                  <Select.Value />
                  <Select.Indicator />
                </Select.Trigger>
                <Select.Popover>
                  <ListBox>
                    <ListBox.Item id="active">活跃</ListBox.Item>
                    <ListBox.Item id="archived">已归档</ListBox.Item>
                  </ListBox>
                </Select.Popover>
              </Select>
              <TextArea
                aria-label="项目描述"
                value={editDescription}
                onChange={(event) => setEditDescription(event.target.value)}
                rows={3}
                fullWidth
                disabled={busy}
                className="rounded-xl border-separator/80 bg-background/40 text-sm md:col-span-2"
              />
              <div className="flex gap-2 md:col-span-2">
                <Button type="submit" variant="primary" size="sm" isDisabled={busy || !editName.trim()}>
                  保存
                </Button>
                <Button variant="ghost" size="sm" onPress={() => setEditing(false)}>
                  取消
                </Button>
              </div>
            </form>
          </Card.Content>
        </Card>
      ) : null}

      <div className="grid gap-5 xl:grid-cols-[minmax(0,1fr)_minmax(0,1.25fr)]">
        <section>
          <div className="mb-3 flex items-center justify-between gap-2">
            <div>
              <h2 className="flex items-center gap-1.5 text-sm font-semibold">
                <GitBranch size={15} />
                仓库
              </h2>
              <p className="mt-0.5 text-xs text-muted">绑定后的仓库才能在此项目创建分析。</p>
            </div>
            <Link href="/repositories" className="text-xs text-muted hover:text-foreground">
              管理仓库
            </Link>
          </div>

          <div className="space-y-2">
            {(project.repositories ?? []).length === 0 ? (
              <Card className="border border-dashed border-separator/70">
                <Card.Content className="p-4 text-sm text-muted">
                  还没有绑定仓库。先在仓库页注册 GitHub 仓库，再回到这里绑定。
                </Card.Content>
              </Card>
            ) : (
              (project.repositories ?? []).map((repository) => (
                <Card key={repository.repository_id} className="border border-separator/60">
                  <Card.Content className="flex items-start gap-3 p-3.5">
                    <GitBranch size={16} className="mt-0.5 shrink-0 text-muted" />
                    <div className="min-w-0 flex-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <Link
                          href={`/repositories/${repository.repository_id}`}
                          className="truncate text-sm font-medium hover:text-accent"
                        >
                          {repositoryLabel(repository.remote_url)}
                        </Link>
                        {repository.is_primary ? (
                          <Chip size="sm" variant="soft" color="accent">
                            <Chip.Label>主仓库</Chip.Label>
                          </Chip>
                        ) : null}
                        <StatusChip status={repository.repository_status} />
                      </div>
                      <p className="mt-1 truncate font-mono text-[10px] text-muted">
                        {repository.remote_url}
                      </p>
                      <p className="mt-1 text-[10px] text-muted">
                        {repository.tracked_refs.length} 个跟踪 ref · {repository.object_format}
                      </p>
                    </div>
                    <Button
                      isIconOnly
                      variant="ghost"
                      size="sm"
                      aria-label="解绑仓库"
                      isDisabled={busy}
                      onPress={() => void unbindRepository(repository.repository_id)}
                    >
                      <X size={14} />
                    </Button>
                  </Card.Content>
                </Card>
              ))
            )}
          </div>

          <Card className="mt-3 border border-separator/60">
            <Card.Content className="space-y-3 p-4">
              <p className="text-xs font-medium text-muted">绑定已有仓库</p>
              {unbound.length > 0 ? (
                <>
                  <Select
                    aria-label="选择要绑定的仓库"
                    selectedKey={selectedRepository || null}
                    onSelectionChange={(key) => setSelectedRepository(String(key ?? ""))}
                    isDisabled={busy}
                    placeholder="选择仓库"
                  >
                    <Select.Trigger>
                      <Select.Value />
                      <Select.Indicator />
                    </Select.Trigger>
                    <Select.Popover>
                      <ListBox>
                        {unbound.map((repository) => (
                          <ListBox.Item key={repository.repository_id} id={repository.repository_id}>
                            {repositoryLabel(repository.remote_url)}
                          </ListBox.Item>
                        ))}
                      </ListBox>
                    </Select.Popover>
                  </Select>
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <Switch isSelected={primary} onChange={setPrimary} size="sm" aria-label="设为主仓库">
                      <Switch.Content>
                        <Switch.Control>
                          <Switch.Thumb />
                        </Switch.Control>
                        <span className="text-[11px] text-muted">设为主仓库</span>
                      </Switch.Content>
                    </Switch>
                    <Button
                      variant="secondary"
                      size="sm"
                      className="gap-1.5"
                      isDisabled={busy || !selectedRepository || project.status !== "active"}
                      onPress={() => void bindRepository()}
                    >
                      <Link2 size={14} />
                      绑定
                    </Button>
                  </div>
                </>
              ) : (
                <div className="flex items-center justify-between gap-2">
                  <p className="text-[11px] text-muted">没有可绑定的仓库。</p>
                  <Link href="/repositories">
                    <Button variant="ghost" size="sm" className="gap-1">
                      注册仓库
                      <ArrowRight size={13} />
                    </Button>
                  </Link>
                </div>
              )}
            </Card.Content>
          </Card>
        </section>

        <section>
          <div className="mb-3 flex items-center justify-between gap-2">
            <div>
              <h2 className="text-sm font-semibold">分析记录</h2>
              <p className="mt-0.5 text-xs text-muted">每条记录固定当时的源码快照。</p>
            </div>
            <Link href={`/new?project=${project.project_id}`} className="text-xs text-muted hover:text-foreground">
              新建分析
            </Link>
          </div>

          {analyses && analyses.length > 0 ? (
            <div className="divide-y divide-separator/50 overflow-hidden rounded-xl border border-separator/60 bg-surface/20">
              {analyses.map((analysis) => {
                const href = analysis.agent_id ? `/tasks/${analysis.agent_id}` : null;
                const content = (
                  <>
                    <StatusChip status={analysis.status} />
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-sm">{analysis.title}</span>
                      <span className="mt-0.5 block text-[11px] text-muted">
                        {analysisTargetLabel(analysis)} · {formatRelative(analysis.created_at)}
                      </span>
                    </span>
                    {href ? <ArrowRight size={14} className="shrink-0 text-muted" /> : null}
                  </>
                );
                return href ? (
                  <Link
                    key={analysis.analysis_id}
                    href={href}
                    className="flex items-center gap-3 px-4 py-3 transition-colors hover:bg-default/40"
                  >
                    {content}
                  </Link>
                ) : (
                  <div key={analysis.analysis_id} className="flex items-center gap-3 px-4 py-3">
                    {content}
                  </div>
                );
              })}
            </div>
          ) : (
            <Card className="border border-dashed border-separator/70">
              <Card.Content className="p-5 text-sm text-muted">
                还没有分析记录。
                {project.status === "active" ? (
                  <Link href={`/new?project=${project.project_id}`} className="ml-1 text-accent hover:underline">
                    创建第一次分析
                  </Link>
                ) : null}
              </Card.Content>
            </Card>
          )}
        </section>
      </div>
    </div>
  );
}
