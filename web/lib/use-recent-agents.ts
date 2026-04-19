"use client";

import { useCallback, useEffect, useState } from "react";

import { api } from "./api";
import { loadRecent } from "./storage";
import type { AgentView, RecentAnalysis } from "./types";

export function useRecentAgents(limit = 8) {
  const [agents, setAgents] = useState<AgentView[]>([]);
  const [recent, setRecent] = useState<RecentAnalysis[]>([]);
  const [repositoryURLs, setRepositoryURLs] = useState<Record<string, string>>({});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(
    async (showLoading = false) => {
      if (showLoading) setLoading(true);
      try {
        const [response, repositories] = await Promise.all([
          api.listAgents(limit),
          api.listRepositories(200).catch(() => null),
        ]);
        setAgents(response.agents ?? []);
        setRecent(loadRecent());
        if (repositories) {
          setRepositoryURLs(
            Object.fromEntries(
              repositories.repositories.map((repository) => [
                repository.repository_id,
                repository.remote_url,
              ]),
            ),
          );
        }
        setError(null);
      } catch (caught) {
        setError(caught instanceof Error ? caught.message : "加载任务失败");
      } finally {
        setLoading(false);
      }
    },
    [limit],
  );

  useEffect(() => {
    void load(true);
    const refresh = () => void load(false);
    const onVisible = () => {
      if (document.visibilityState === "visible") refresh();
    };
    window.addEventListener("focus", refresh);
    window.addEventListener("sentinel:agents-changed", refresh);
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      window.removeEventListener("focus", refresh);
      window.removeEventListener("sentinel:agents-changed", refresh);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [load]);

  return {
    agents,
    recent,
    repositoryURLs,
    loading,
    error,
    reload: () => void load(true),
  };
}
