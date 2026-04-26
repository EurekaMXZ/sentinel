"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import {
  Alert,
  Button,
  Card,
  Input,
  ListBox,
  Select,
  Skeleton,
  Spinner,
  TextArea,
} from "@heroui/react";
import {
  ArrowRight,
  CircleAlert,
  GitBranch,
  Plus,
  ScanSearch,
} from "lucide-react";

import { ApiError, api } from "@/lib/api";
import { DEFAULT_TASK } from "@/lib/config";
import { formatDateTime, repositoryLabel } from "@/lib/format";
import { addRecent } from "@/lib/storage";
import type { Project, Snapshot } from "@/lib/types";
import { StatusChip } from "./status-chip";

function notifyAgentsChanged(): void {
  window.dispatchEvent(new Event("sentinel:agents-changed"));
}

export function NewAnalysis() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const initialProject = searchParams.get("project") ?? "";
  const initialRepository = searchParams.get("repository") ?? "";

  const [projects, setProjects] = useState<Project[] | null>(null);
  const [projectsError, setProjectsError] = useState<string | null>(null);
  const [projectId, setProjectId] = useState("");
  const [project, setProject] = useState<Project | null>(null);
  const [projectLoading, setProjectLoading] = useState(false);
  const [repoId, setRepoId] = useState("");
  const [versions, setVersions] = useState<Snapshot[] | null>(null);
  const [versionsLoading, setVersionsLoading] = useState(false);
  const [snapshotId, setSnapshotId] = useState("");
  const [title, setTitle] = useState("");
  const [task, setTask] = useState(DEFAULT_TASK);
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    let active = true;
    api
      .listProjects(200)
      .then((page) => {
        if (!active) return;
        setProjects(page.projects);
        const preferred =
          initialProject && page.projects.some((item) => item.project_id === initialProject)
            ? initialProject
            : page.projects.find((item) => item.status === "active")?.project_id ??
              page.projects[0]?.project_id ??
              "";
        setProjectId((current) => current || preferred);
      })
      .catch((caught) => {
        if (active) setProjectsError(caught instanceof Error ? caught.message : "加载项目失败");
      });
    return () => {
      active = false;
    };
  }, [initialProject]);

  useEffect(() => {
    if (!projectId) {
      setProject(null);
      setRepoId("");
      setVersions([]);
      setSnapshotId("");
      return;
    }
    let active = true;
    setProject(null);
    setRepoId("");
    setVersions([]);
    setSnapshotId("");
    setProjectLoading(true);
    api
      .getProject(projectId)
      .then((next) => {
        if (!active) return;
        setProject(next);
        const bound = next.repositories ?? [];
        const preferred =
          initialRepository && bound.some((item) => item.repository_id === initialRepository)
            ? initialRepository
            : bound.find((item) => item.repository_status === "active")?.repository_id ??
              bound[0]?.repository_id ??
              "";
        setRepoId(preferred);
      })
      .catch((caught) => {
        if (active) setError(caught instanceof Error ? caught.message : "加载项目失败");
      })
      .finally(() => {
        if (active) setProjectLoading(false);
      });
    return () => {
      active = false;
    };
  }, [projectId, initialRepository]);

  useEffect(() => {
    if (!repoId) {
      setVersions([]);
      setSnapshotId("");
      return;
    }
    let active = true;
    setVersionsLoading(true);
    setSnapshotId("");
    api
      .listVersions(repoId, 100)
      .then((page) => {
        if (!active) return;
        setVersions(page.versions);
        setSnapshotId(page.versions[0]?.source_snapshot_id ?? "");
      })
      .catch((caught) => {
        if (active) setError(caught instanceof Error ? caught.message : "加载源码版本失败");
      })
      .finally(() => {
        if (active) setVersionsLoading(false);
      });
    return () => {
      active = false;
    };
  }, [repoId]);

  const boundRepositories = project?.repositories ?? [];
  const selectedRepository = boundRepositories.find((item) => item.repository_id === repoId);
  const selectedSnapshot = versions?.find((item) => item.source_snapshot_id === snapshotId);

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!project || !repoId || !snapshotId) return;
    setSubmitting(true);
    setError(null);
    const prompt = task.trim() || DEFAULT_TASK;
    try {
      const analysis = await api.createAnalysis(project.project_id, {
        repository_id: repoId,
        snapshot_id: snapshotId,
        task: prompt,
        ...(title.trim() ? { title: title.trim() } : {}),
      });
      if (analysis.agent_id) {
        addRecent({
          agentId: analysis.agent_id,
          url: selectedRepository?.remote_url ?? "",
          task: prompt,
          createdAt: new Date().toISOString(),
        });
        notifyAgentsChanged();
        router.push(`/tasks/${analysis.agent_id}`);
      } else {
        router.push(`/projects/${project.project_id}`);
      }
    } catch (caught) {
      if (
        caught instanceof ApiError &&
        caught.code === "workflow_start_failed" &&
        typeof caught.body.agent_id === "string"
      ) {
        addRecent({
          agentId: caught.body.agent_id,
          url: selectedRepository?.remote_url ?? "",
          task: prompt,
          createdAt: new Date().toISOString(),
        });
        notifyAgentsChanged();
        router.push(`/tasks/${caught.body.agent_id}?resume=1`);
        return;
      }
      setError(caught instanceof Error ? caught.message : "创建分析失败");
    } finally {
      setSubmitting(false);
    }
  }

  if (projects === null && !projectsError) {
    return (
      <div className="mx-auto w-full max-w-[900px] space-y-4 px-4 py-10 sm:px-6">
        <Skeleton className="h-8 w-56 rounded-lg" />
        <Skeleton className="h-64 w-full rounded-xl" />
      </div>
    );
  }

  if (projectsError) {
    return (
      <div className="mx-auto w-full max-w-3xl px-4 py-16 sm:px-6">
        <Alert status="danger">
          <Alert.Indicator>
            <CircleAlert size={16} />
          </Alert.Indicator>
          <Alert.Content>
            <Alert.Title>无法加载项目</Alert.Title>
            <Alert.Description>{projectsError}</Alert.Description>
          </Alert.Content>
        </Alert>
      </div>
    );
  }

  if (!projects || projects.length === 0) {
    return (
      <div className="mx-auto w-full max-w-3xl px-4 py-16 sm:px-6">
        <Card className="border border-dashed border-separator/70">
          <Card.Content className="flex flex-col items-start gap-3 p-6">
            <h1 className="text-xl font-semibold">先创建一个项目</h1>
            <p className="text-sm text-muted">分析必须归属于项目；创建项目并绑定仓库后即可选择版本。</p>
            <Link href="/projects">
              <Button variant="primary" className="gap-1.5">
                <Plus size={15} />
                创建项目
              </Button>
            </Link>
          </Card.Content>
        </Card>
      </div>
    );
  }

  return (
    <div className="mx-auto w-full max-w-[1000px] px-4 py-8 sm:px-6 lg:py-12">
      <div>
        <p className="flex items-center gap-1.5 text-xs text-muted">
          <ScanSearch size={13} />
          只读源码分析
        </p>
        <h1 className="mt-1 text-2xl font-semibold tracking-tight">新建分析</h1>
        <p className="mt-1 max-w-2xl text-sm text-muted">
          选择项目、仓库和具体快照。分析创建后会固定该 commit，后续分支更新不会影响这次分析。
        </p>
      </div>

      <Card className="mt-6 border border-separator/60">
        <Card.Content className="p-5 sm:p-6">
          <form onSubmit={submit} className="space-y-5">
            <div className="grid gap-4 md:grid-cols-2">
              <label className="block">
                <span className="mb-1.5 block text-xs font-medium text-muted">项目</span>
                <Select
                  aria-label="选择项目"
                  selectedKey={projectId || null}
                  onSelectionChange={(key) => setProjectId(String(key ?? ""))}
                  placeholder="选择项目"
                  isDisabled={submitting}
                >
                  <Select.Trigger>
                    <Select.Value />
                    <Select.Indicator />
                  </Select.Trigger>
                  <Select.Popover>
                    <ListBox>
                      {projects.map((item) => (
                        <ListBox.Item key={item.project_id} id={item.project_id}>
                          {item.name}
                        </ListBox.Item>
                      ))}
                    </ListBox>
                  </Select.Popover>
                </Select>
              </label>

              <label className="block">
                <span className="mb-1.5 block text-xs font-medium text-muted">仓库</span>
                <Select
                  aria-label="选择仓库"
                  selectedKey={repoId || null}
                  onSelectionChange={(key) => setRepoId(String(key ?? ""))}
                  placeholder={projectLoading ? "加载中…" : "选择仓库"}
                  isDisabled={submitting || projectLoading || boundRepositories.length === 0}
                >
                  <Select.Trigger>
                    <Select.Value />
                    <Select.Indicator />
                  </Select.Trigger>
                  <Select.Popover>
                    <ListBox>
                      {boundRepositories.map((item) => (
                        <ListBox.Item key={item.repository_id} id={item.repository_id}>
                          {repositoryLabel(item.remote_url)}
                        </ListBox.Item>
                      ))}
                    </ListBox>
                  </Select.Popover>
                </Select>
              </label>
            </div>

            {project && project.status !== "active" ? (
              <Alert status="warning">
                <Alert.Indicator>
                  <CircleAlert size={16} />
                </Alert.Indicator>
                <Alert.Content>
                  <Alert.Title>项目已归档</Alert.Title>
                  <Alert.Description>归档项目不能创建新分析，请先在项目详情中恢复为活跃状态。</Alert.Description>
                  <div className="mt-3">
                    <Link href={`/projects/${project.project_id}`}>
                      <Button variant="secondary" size="sm">打开项目</Button>
                    </Link>
                  </div>
                </Alert.Content>
              </Alert>
            ) : null}

            {project && boundRepositories.length === 0 ? (
              <Alert status="warning">
                <Alert.Indicator>
                  <CircleAlert size={16} />
                </Alert.Indicator>
                <Alert.Content>
                  <Alert.Title>该项目还没有绑定仓库</Alert.Title>
                  <Alert.Description>
                    先在项目详情中绑定仓库，或去仓库页注册并同步一个 GitHub 仓库。
                  </Alert.Description>
                  <div className="mt-3 flex gap-2">
                    <Link href={`/projects/${project.project_id}`}>
                      <Button variant="secondary" size="sm">打开项目</Button>
                    </Link>
                    <Link href="/repositories">
                      <Button variant="ghost" size="sm">管理仓库</Button>
                    </Link>
                  </div>
                </Alert.Content>
              </Alert>
            ) : null}

            {selectedRepository && selectedRepository.repository_status !== "active" ? (
              <Alert status="warning">
                <Alert.Indicator>
                  <CircleAlert size={16} />
                </Alert.Indicator>
                <Alert.Content>
                  <Alert.Title>仓库当前不可用于分析</Alert.Title>
                  <Alert.Description>
                    仓库状态为 {selectedRepository.repository_status}，请到仓库详情切换为活跃状态。
                  </Alert.Description>
                  <div className="mt-3">
                    <Link href={`/repositories/${selectedRepository.repository_id}`}>
                      <Button variant="secondary" size="sm">打开仓库</Button>
                    </Link>
                  </div>
                </Alert.Content>
              </Alert>
            ) : null}

            {repoId ? (
              <div>
                <div className="mb-1.5 flex items-center justify-between gap-2">
                  <span className="text-xs font-medium text-muted">源码版本</span>
                  <Link href={`/repositories/${repoId}`} className="text-[11px] text-muted hover:text-foreground">
                    查看同步状态
                  </Link>
                </div>
                <Select
                  aria-label="选择源码版本"
                  selectedKey={snapshotId || null}
                  onSelectionChange={(key) => setSnapshotId(String(key ?? ""))}
                  placeholder={versionsLoading ? "加载版本…" : "选择 commit 快照"}
                  isDisabled={submitting || versionsLoading || (versions?.length ?? 0) === 0}
                >
                  <Select.Trigger>
                    <Select.Value />
                    <Select.Indicator />
                  </Select.Trigger>
                  <Select.Popover>
                    <ListBox>
                      {(versions ?? []).map((snapshot) => (
                        <ListBox.Item key={snapshot.source_snapshot_id} id={snapshot.source_snapshot_id}>
                          {`${snapshot.commit_sha.slice(0, 12)} · ${formatDateTime(snapshot.created_at)}`}
                        </ListBox.Item>
                      ))}
                    </ListBox>
                  </Select.Popover>
                </Select>
                {versions && versions.length === 0 ? (
                  <p className="mt-2 text-[11px] text-warning">
                    该仓库还没有就绪快照。请到仓库详情执行同步，或查看同步是否失败。
                  </p>
                ) : null}
                {selectedSnapshot ? (
                  <p className="mt-2 flex flex-wrap items-center gap-2 text-[11px] text-muted">
                    <GitBranch size={12} />
                    <span className="font-mono">{selectedSnapshot.commit_sha}</span>
                    <span>·</span>
                    <span>{formatDateTime(selectedSnapshot.created_at)}</span>
                  </p>
                ) : null}
              </div>
            ) : null}

            <div>
              <span className="mb-1.5 block text-xs font-medium text-muted">分析标题（可选）</span>
              <Input
                aria-label="分析标题"
                placeholder="留空自动生成，例如 owner/repo @ commit"
                value={title}
                onChange={(event) => setTitle(event.target.value)}
                maxLength={300}
                disabled={submitting}
                className="w-full rounded-xl border-separator/80 bg-background/40 text-sm"
              />
            </div>

            <div>
              <span className="mb-1.5 block text-xs font-medium text-muted">分析要求</span>
              <TextArea
                aria-label="分析要求"
                value={task}
                onChange={(event) => setTask(event.target.value)}
                rows={6}
                fullWidth
                disabled={submitting}
                className="rounded-xl border-separator/80 bg-background/40 text-sm"
              />
              <p className="mt-2 text-[11px] text-muted">
                默认要求关注架构、核心模块、数据流、风险与源码证据。Agent 只读，不会修改仓库。
              </p>
            </div>

            {error ? (
              <Alert status="danger">
                <Alert.Indicator>
                  <CircleAlert size={16} />
                </Alert.Indicator>
                <Alert.Content>
                  <Alert.Description>{error}</Alert.Description>
                </Alert.Content>
              </Alert>
            ) : null}

            <div className="flex flex-wrap items-center justify-between gap-3 border-t border-separator/60 pt-4">
              <div className="flex items-center gap-2 text-[11px] text-muted">
                {project ? <StatusChip status={project.status} /> : null}
                {selectedRepository ? (
                  <span className="font-mono">{repositoryLabel(selectedRepository.remote_url)}</span>
                ) : null}
              </div>
              <Button
                type="submit"
                variant="primary"
                className="gap-1.5"
                isDisabled={
                  submitting ||
                  !project ||
                  !repoId ||
                  !snapshotId ||
                  project.status !== "active" ||
                  selectedRepository?.repository_status !== "active"
                }
              >
                {submitting ? <Spinner size="sm" color="current" /> : <ArrowRight size={15} />}
                {submitting ? "创建中…" : "开始分析"}
              </Button>
            </div>
          </form>
        </Card.Content>
      </Card>
    </div>
  );
}
