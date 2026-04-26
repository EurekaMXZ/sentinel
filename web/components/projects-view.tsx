"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { Alert, Button, Card, Input, Skeleton } from "@heroui/react";
import { ArrowRight, CircleAlert, FolderKanban, Plus, RefreshCw } from "lucide-react";

import { api } from "@/lib/api";
import { formatRelative } from "@/lib/format";
import type { Project } from "@/lib/types";
import { StatusChip } from "./status-chip";

export function ProjectsView() {
  const [projects, setProjects] = useState<Project[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [creating, setCreating] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);

  async function load() {
    setLoading(true);
    try {
      const page = await api.listProjects(200);
      setProjects(page.projects);
      setError(null);
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "加载项目失败");
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
    setNotice(null);
    setError(null);
    try {
      const project = await api.createProject({
        name: name.trim(),
        description: description.trim(),
      });
      setProjects((current) => [project, ...(current ?? [])]);
      setName("");
      setDescription("");
      setNotice(`项目「${project.name}」已创建`);
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "创建项目失败");
    } finally {
      setCreating(false);
    }
  }

  return (
    <div className="mx-auto w-full max-w-[1180px] px-4 py-8 sm:px-6 lg:py-10">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <p className="flex items-center gap-1.5 text-xs text-muted">
            <FolderKanban size={13} />
            控制面
          </p>
          <h1 className="mt-1 text-2xl font-semibold tracking-tight">项目</h1>
          <p className="mt-1 max-w-2xl text-sm text-muted">
            项目承载仓库绑定和分析记录。一个仓库可以绑定到多个项目，源码同步和快照仍然复用同一份仓库资源。
          </p>
        </div>
        <Button variant="secondary" size="sm" className="gap-1.5" onPress={() => void load()}>
          <RefreshCw size={14} />
          刷新
        </Button>
      </div>

      <Card className="mt-6 border border-separator/60">
        <Card.Content className="p-4 sm:p-5">
          <form onSubmit={create} className="grid gap-3 md:grid-cols-[minmax(0,1fr)_minmax(0,1.5fr)_auto] md:items-end">
            <Input
              aria-label="项目名称"
              placeholder="项目名称"
              value={name}
              onChange={(event) => setName(event.target.value)}
              maxLength={200}
              disabled={creating}
              className="rounded-xl border-separator/80 bg-background/40 text-sm"
            />
            <Input
              aria-label="项目描述"
              placeholder="项目描述（可选）"
              value={description}
              onChange={(event) => setDescription(event.target.value)}
              maxLength={4000}
              disabled={creating}
              className="rounded-xl border-separator/80 bg-background/40 text-sm"
            />
            <Button
              type="submit"
              variant="primary"
              className="gap-1.5"
              isDisabled={creating || !name.trim()}
            >
              <Plus size={15} />
              {creating ? "创建中…" : "创建项目"}
            </Button>
          </form>
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
        {loading && projects === null ? (
          <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
            {[0, 1, 2, 3].map((item) => (
              <Skeleton key={item} className="h-36 rounded-xl" />
            ))}
          </div>
        ) : projects && projects.length > 0 ? (
          <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
            {projects.map((project) => (
              <Link key={project.project_id} href={`/projects/${project.project_id}`}>
                <Card className="h-full border border-separator/60 transition-colors hover:border-accent/50">
                  <Card.Content className="flex h-full flex-col p-4">
                    <div className="flex items-start justify-between gap-3">
                      <div className="min-w-0">
                        <p className="truncate text-sm font-medium">{project.name}</p>
                        <p className="mt-1 line-clamp-2 min-h-8 text-xs leading-5 text-muted">
                          {project.description || "暂无项目描述"}
                        </p>
                      </div>
                      <StatusChip status={project.status} />
                    </div>
                    <div className="mt-auto flex items-center justify-between gap-3 pt-4 text-[11px] text-muted">
                      <span>{project.repository_count} 仓库 · {project.analysis_count} 分析</span>
                      <span className="flex items-center gap-1">
                        {formatRelative(project.updated_at)}
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
              还没有项目。在上方输入名称创建一个，然后进入项目绑定仓库。
            </Card.Content>
          </Card>
        )}
      </div>
    </div>
  );
}
