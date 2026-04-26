export type AgentStatus =
  | "pending"
  | "running"
  | "waiting"
  | "completed"
  | "failed"
  | "cancelled";

export type ResourceKind = "source_repository" | "source_snapshot" | "object";

export interface AgentResource {
  binding_name: string;
  resource_kind: ResourceKind;
  repository_id?: string;
  source_snapshot_id?: string;
  object_id?: string;
}

export interface AgentView {
  tenant_id: string;
  session_id: string;
  agent_id: string;
  workflow_id: string;
  parent_agent_id?: string;
  parent_tool_execution_id?: string;
  status: AgentStatus;
  result_ref?: string;
  error_ref?: string;
  row_version: number;
  context_version: number;
  last_event_sequence: number;
  next_step_id: number;
  created_at: string;
  started_at?: string;
  finished_at?: string;
  resources?: AgentResource[];
}

export interface SourceRef {
  repository_id: string;
  source_snapshot_id: string;
}

export interface AgentResult {
  agent_id: string;
  status: AgentStatus;
  result_ref?: string;
  summary?: string;
  response_ref?: string;
  artifacts?: unknown[];
  source_refs?: SourceRef[];
  error_ref?: string;
  error?: unknown;
}

export interface Repository {
  repository_id: string;
  tenant_id: string;
  remote_url: string;
  credential_ref?: string;
  tracked_refs: string[];
  object_format: string;
  archive_head_id?: string;
  archive_version: number;
  status: string;
}

export interface Snapshot {
  source_snapshot_id: string;
  repository_id: string;
  commit_sha: string;
  tree_sha: string;
  storage_manifest_id: string;
  status: string;
  created_at: string;
}

export interface SyncView {
  tenant_id: string;
  repository_id: string;
  sync_id: string;
  workflow_id: string;
  status: string;
  result_ref?: string;
  error_ref?: string;
  manifest_id?: string;
  snapshot_count?: number;
  snapshots?: Snapshot[];
  error?: unknown;
}

export interface VersionPage {
  repository_id: string;
  versions: Snapshot[];
  more: boolean;
  offset: number;
  limit: number;
  next_offset?: number;
}

export interface AgentEvent {
  /** Redis Stream ID, also used as the SSE id for replay/reconnect. */
  id: string;
  type: string;
  step_id?: number;
  attempt?: number;
  data?: unknown;
  created_at: string;
}

export interface CreateAgentRequest {
  task: string;
  resources?: AgentResource[];
  context_ref?: string;
  expected_output?: string;
  max_steps?: number;
  max_child_depth?: number;
}

export interface ProjectRepository {
  repository_id: string;
  remote_url: string;
  repository_status: string;
  tracked_refs: string[];
  object_format: string;
  is_primary: boolean;
  bound_at: string;
}

export interface Project {
  project_id: string;
  tenant_id: string;
  name: string;
  description: string;
  status: string;
  repository_count: number;
  analysis_count: number;
  repositories?: ProjectRepository[];
  created_at: string;
  updated_at: string;
}

export interface AnalysisTarget {
  target_id: string;
  kind: "snapshot" | "commit" | "ref" | "pull_request" | string;
  ref_name?: string;
  commit_sha?: string;
  base_commit_sha?: string;
  pull_request_number?: number;
  source_snapshot_id?: string;
  base_snapshot_id?: string;
  status: string;
}

export interface Analysis {
  analysis_id: string;
  tenant_id: string;
  project_id: string;
  repository_id: string;
  session_id: string;
  title: string;
  trigger: string;
  status: string;
  agent_id?: string;
  workflow_id?: string;
  target?: AnalysisTarget;
  created_at: string;
}

export interface RefRecord {
  ref_name: string;
  target_oid?: string;
  commit_sha?: string;
  sync_id: string;
  observed_at: string;
  source_snapshot_id?: string;
  snapshot_status?: string;
}

export interface SyncRecord {
  sync_id: string;
  status: string;
  trigger_type: string;
  created_at: string;
  started_at?: string;
  finished_at?: string;
  result_ref?: string;
  error_ref?: string;
}

export interface RecentAnalysis {
  agentId: string;
  url: string;
  task: string;
  createdAt: string;
}
