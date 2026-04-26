"use client";

import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { Alert, Button, Card, Chip, Skeleton, Spinner } from "@heroui/react";
import {
  ArrowRight,
  CircleAlert,
  FolderKanban,
  GitBranch,
  Plus,
  ScanSearch,
} from "lucide-react";

import { api } from "@/lib/api";
import { formatRelative, repositoryLabel, statusLabel, statusTone } from "@/lib/format";
import type { Project } from "@/lib/types";
import { useRecentAgents } from "@/lib/use-recent-agents";
import { StatusChip } from "./status-chip";

export function Dashboard() {
  const [projects, setProjects] = useState<Project[] | null>(null);
  const [projectError, setProjectError] = useState<string | null>(null);
  const { agents, recent, repositoryURLs, loading: agentsLoading } = useRecentAgents(8);

  useEffect(() => {
    let active = true;
    api
      .listProjects(6)
      .then((page) => {
        if (active) setProjects(page.projects);
      })
      .catch((caught) => {
        if (active) setProjectError(caught instanceof Error ? caught.message : "加载项目失败");
      });
    return () => {
      active = false;
    };
  }, []);

  const recentAgents = useMemo(() => agents.slice(0, 6), [agents]);

  return (
    <div className="mx-auto w-full max-w-[1180px] px-4 py-8 sm:px-6 lg:py-12">
      <section className="flex flex-col gap-6 lg:flex-row lg:items-end lg:justify-between">
        <div className="max-w-2xl">
          <div className="mb-3 flex flex-wrap items-center gap-2">
            <Chip color="accent" variant="soft" size="sm">
              <Chip.Label>SOURCE ANALYSIS</Chip.Label>
            </Chip>
            <span className="flex items-center gap-1 text-xs text-muted">
              GitHub
              <ArrowRight size={12} />
              项目
              <ArrowRight size={12} />
              仓库
              <ArrowRight size={12} />
              分析
            </span>
          </div>
          <h1 className="text-3xl font-semibold tracking-tight sm:text-4xl">
            从项目和版本开始，
            <span className="text-accent">留下可核对的分析记录</span>
          </h1>
          <p className="mt-3 text-sm leading-6 text-muted sm:text-base">
            将 GitHub 仓库绑定到项目，选择具体源码版本发起只读分析。
            每次分析都会固定对应的 commit 快照，后续分支更新不会改变已有结论。
          </p>
        </div>
        <div className="flex shrink-0 flex-wrap gap-2">
          <Link href="/new">
            <Button variant="primary" className="gap-1.5">
              <Plus size={16} />
              新建分析
            </Button>
          </Link>
          <Link href="/projects">
            <Button variant="secondary" className="gap-1.5">
              <FolderKanban size={16} />
              管理项目
            </Button>
          </Link>
          <Link href="/repositories">
            <Button variant="secondary" className="gap-1.5">
              <GitBranch size={16} />
              管理仓库
            </Button>
          </Link>
        </div>
      </section>

      {projectError ? (
        <Alert status="danger" className="mt-6">
          <Alert.Indicator>
            <CircleAlert size={16} />
          </Alert.Indicator>
          <Alert.Content>
            <Alert.Title>项目加载失败</Alert.Title>
            <Alert.Description>{projectError}</Alert.Description>
          </Alert.Content>
        </Alert>
      ) : null}

      <section className="mt-9">
        <div className="mb-3 flex items-center justify-between gap-3">
          <div>
            <h2 className="text-sm font-semibold">项目</h2>
            <p className="mt-0.5 text-xs text-muted">项目把仓库、分析记录和后续的 Actions 回调放在一起。</p>
          </div>
          <Link href="/projects" className="text-xs text-muted hover:text-foreground">
            全部项目
          </Link>
        </div>

        {projects === null && !projectError ? (
          <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
            {[0, 1, 2].map((item) => (
              <Skeleton key={item} className="h-32 rounded-xl" />
            ))}
          </div>
        ) : projects && projects.length > 0 ? (
          <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
            {projects.map((project) => (
              <Link key={project.project_id} href={`/projects/${project.project_id}`}>
                <Card className="h-full border border-separator/60 transition-colors hover:border-accent/50">
                  <Card.Content className="p-4">
                    <div className="flex items-start justify-between gap-3">
                      <div className="min-w-0">
                        <p className="truncate text-sm font-medium">{project.name}</p>
                        <p className="mt-1 line-clamp-2 min-h-8 text-xs leading-5 text-muted">
                          {project.description || "暂无项目描述"}
                        </p>
                      </div>
                      <StatusChip status={project.status} />
                    </div>
                    <div className="mt-4 flex items-center gap-4 text-[11px] text-muted">
                      <span className="flex items-center gap-1">
                        <GitBranch size={12} />
                        {project.repository_count} 仓库
                      </span>
                      <span className="flex items-center gap-1">
                        <ScanSearch size={12} />
                        {project.analysis_count} 分析
                      </span>
                      <span className="ml-auto">{formatRelative(project.updated_at)}</span>
                    </div>
                  </Card.Content>
                </Card>
              </Link>
            ))}
          </div>
        ) : (
          <Card className="border border-dashed border-separator/70">
            <Card.Content className="flex flex-col items-start gap-3 p-5">
              <p className="text-sm text-muted">还没有项目。先创建一个项目，再把 GitHub 仓库绑定进去。</p>
              <Link href="/projects">
                <Button variant="primary" size="sm">
                  创建第一个项目
                </Button>
              </Link>
            </Card.Content>
          </Card>
        )}
      </section>

      <section className="mt-9">
        <div className="mb-3 flex items-center justify-between gap-3">
          <div>
            <h2 className="text-sm font-semibold">最近分析</h2>
            <p className="mt-0.5 text-xs text-muted">打开任意一条记录查看执行日志和最终报告。</p>
          </div>
          {agentsLoading ? <Spinner size="sm" /> : null}
        </div>
        {recentAgents.length > 0 ? (
          <div className="divide-y divide-separator/50 rounded-xl border border-separator/60 bg-surface/20">
            {recentAgents.map((agent) => {
              const meta = recent.find((item) => item.agentId === agent.agent_id);
              const repositoryId = agent.resources?.find((resource) => resource.repository_id)?.repository_id;
              const label =
                repositoryLabel(meta?.url) ||
                repositoryLabel(repositoryId ? repositoryURLs[repositoryId] : "") ||
                meta?.task ||
                `Agent ${agent.agent_id.slice(0, 10)}`;
              return (
                <Link
                  key={agent.agent_id}
                  href={`/tasks/${agent.agent_id}`}
                  className="flex items-center gap-3 px-4 py-3 transition-colors hover:bg-default/40"
                >
                  <StatusChip status={agent.status} />
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-sm">{label}</span>
                    <span className="mt-0.5 block text-[11px] text-muted">
                      {meta?.task ?? "源码分析"} · {formatRelative(agent.created_at)}
                    </span>
                  </span>
                  <span
                    className={`shrink-0 text-xs ${
                      statusTone(agent.status) === "danger" ? "text-danger" : "text-muted"
                    }`}
                  >
                    {statusLabel(agent.status)}
                  </span>
                  <ArrowRight size={14} className="shrink-0 text-muted" />
                </Link>
              );
            })}
          </div>
        ) : (
          <Card className="border border-dashed border-separator/70">
            <Card.Content className="p-5 text-sm text-muted">
              还没有分析记录。选择一个项目和仓库后创建第一次分析。
            </Card.Content>
          </Card>
        )}
      </section>
    </div>
  );
}
