// Types matching Forge Control Plane OpenAPI spec (docs/contracts/control-api.openapi.yaml)
// Stub file -- types defined in GREEN phase

export type SandboxState = 'pending' | 'starting' | 'running' | 'restarting' | 'destroying' | 'destroyed' | 'failed' | 'stopped';

export interface Sandbox {
  id: string;
  app_name: string;
  user_id: string;
  node_id: string | null;
  container_id: string | null;
  image: string;
  state: SandboxState;
  url: string;
  resources: { cpu_cores: number; memory_mb: number };
  host_port: number | null;
  created_at: string;
  updated_at: string;
  last_active_at: string;
  failure_count: number;
  metadata: Record<string, unknown>;
}

export interface SandboxCreate {
  app_name: string;
  user_id: string;
  image: string;
  resources?: { cpu_cores?: number; memory_mb?: number };
  env?: Record<string, string>;
  idle_timeout_seconds?: number;
  metadata?: Record<string, unknown>;
}

export interface Node {
  id: string;
  hostname: string;
  tailscale_ip: string;
  capacity_mb: number;
  used_mb: number;
  capacity_cpu: number;
  status: 'healthy' | 'unhealthy' | 'draining' | 'removed';
  last_seen_at: string;
  registered_at: string;
  agent_version: string;
  running_sandboxes: number;
}

export interface Route {
  app_name: string;
  sandbox_id: string;
  upstream: string;
  updated_at: string;
}

export interface AgentCommand {
  id: string;
  type: 'start_sandbox' | 'stop_sandbox' | 'restart_sandbox' | 'get_logs' | 'prune';
  sandbox_id: string | null;
  payload: Record<string, unknown>;
  issued_at: string;
  timeout_seconds: number;
}

export interface ForgeErrorResponse {
  type: string;
  title: string;
  status: number;
  detail?: string;
  instance?: string;
}

export interface FilePushRequest {
  files: Array<{
    path: string;
    content: string;
    delete?: boolean;
  }>;
}

export interface FilePushResponse {
  written: string[];
  failed: string[];
}

export interface SandboxListFilters {
  app_name?: string;
  user_id?: string;
  state?: SandboxState;
  node_id?: string;
  limit?: number;
}
