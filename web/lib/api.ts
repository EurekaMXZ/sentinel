import { TENANT_ID } from "./config";
import type {
  AgentResult,
  AgentView,
  CreateAgentRequest,
  Repository,
  Snapshot,
  SyncView,
  VersionPage,
} from "./types";

export interface ApiErrorBody {
  error?: string;
  code?: string;
  sync_id?: string;
  agent_id?: string;
  [key: string]: unknown;
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly body: ApiErrorBody;

  constructor(status: number, code: string, message: string, body: ApiErrorBody = {}) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.body = body;
  }
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  headers.set("X-Tenant-ID", TENANT_ID);
  if (init.body !== undefined && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }

  const response = await fetch(path, { ...init, headers });
  if (!response.ok) {
    let body: ApiErrorBody = {};
    try {
      body = (await response.json()) as ApiErrorBody;
    } catch {
      // The status and status text still carry enough context.
    }
    const code = typeof body.code === "string" ? body.code : "request_failed";
    const message =
      typeof body.error === "string" && body.error
        ? body.error
        : `请求失败（HTTP ${response.status}）`;
    throw new ApiError(response.status, code, message, body);
  }
  if (response.status === 204) {
    return undefined as T;
  }
  return (await response.json()) as T;
}

function jsonBody(value: unknown): string {
  return JSON.stringify(value);
}

export const api = {
  health(): Promise<{ status: string; service: string }> {
    return request("/healthz");
  },

  listAgents(limit = 20): Promise<{ agents: AgentView[] }> {
    return request(`/v1/agents?limit=${limit}`);
  },

  getAgent(agentId: string): Promise<AgentView> {
    return request(`/v1/agents/${encodeURIComponent(agentId)}`);
  },

  getAgentResult(agentId: string): Promise<AgentResult> {
    return request(`/v1/agents/${encodeURIComponent(agentId)}/result`);
  },

  createAgent(body: CreateAgentRequest): Promise<AgentView> {
    return request("/v1/agents", { method: "POST", body: jsonBody(body) });
  },

  cancelAgent(agentId: string): Promise<{ agent_id: string; status: string }> {
    return request(`/v1/agents/${encodeURIComponent(agentId)}/cancel`, {
      method: "POST",
    });
  },

  resumeAgent(agentId: string): Promise<AgentView | { agent_id: string; status: string }> {
    return request(`/v1/agents/${encodeURIComponent(agentId)}/resume`, {
      method: "POST",
    });
  },

  listRepositories(limit = 200): Promise<{ repositories: Repository[] }> {
    return request(`/v1/source/repositories?limit=${limit}`);
  },

  createRepository(body: {
    url: string;
    tracked_refs?: string[];
    credential_ref?: string;
    object_format?: string;
  }): Promise<Repository> {
    return request("/v1/source/repositories", {
      method: "POST",
      body: jsonBody(body),
    });
  },

  createSync(repositoryId: string): Promise<SyncView> {
    return request(`/v1/source/repositories/${encodeURIComponent(repositoryId)}/syncs`, {
      method: "POST",
    });
  },

  getSync(syncId: string): Promise<SyncView> {
    return request(`/v1/source/syncs/${encodeURIComponent(syncId)}`);
  },

  resumeSync(syncId: string): Promise<SyncView | { sync_id: string; status: string }> {
    return request(`/v1/source/syncs/${encodeURIComponent(syncId)}/resume`, {
      method: "POST",
    });
  },

  listVersions(repositoryId: string, limit = 1): Promise<VersionPage> {
    return request(
      `/v1/source/repositories/${encodeURIComponent(repositoryId)}/versions?offset=0&limit=${limit}`,
    );
  },

  latestSnapshot(repositoryId: string): Promise<Snapshot | undefined> {
    return api.listVersions(repositoryId, 1).then((page) => page.versions?.[0]);
  },
};
