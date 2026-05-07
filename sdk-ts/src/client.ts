// ForgeClient -- TypeScript SDK for Forge control plane API
// Uses native fetch (Node 18+), no external HTTP dependencies.
// Never logs apiKey (T-06-05 mitigation).
// Always sends credentials via Authorization header, never in URL (T-06-07 mitigation).

import type {
  Sandbox,
  SandboxCreate,
  SandboxListFilters,
  FilePushResponse,
  ForgeErrorResponse,
  Node,
  Route,
} from './types.js';
import {
  ForgeError,
  ForgeNotFoundError,
  ForgeConflictError,
  ForgeServiceError,
} from './errors.js';

export interface ForgeClientConfig {
  baseUrl: string;
  apiKey: string;
}

export class ForgeClient {
  private readonly baseUrl: string;
  private readonly apiKey: string;

  constructor(config: ForgeClientConfig) {
    // Strip trailing slash for consistent URL construction
    this.baseUrl = config.baseUrl.replace(/\/+$/, '');
    this.apiKey = config.apiKey;
  }

  // ── Sandboxes ──────────────────────────────────────────────

  sandboxes = {
    /** Create a new sandbox. Returns the sandbox in 'pending' state. */
    create: async (req: SandboxCreate): Promise<Sandbox> => {
      return this.request<Sandbox>('POST', '/v1/sandboxes', req);
    },

    /** Get a sandbox by UUID or by app_name with "app:" prefix. */
    get: async (idOrApp: string): Promise<Sandbox> => {
      return this.request<Sandbox>('GET', `/v1/sandboxes/${idOrApp}`);
    },

    /** List sandboxes with optional filters. */
    list: async (filters?: SandboxListFilters): Promise<Sandbox[]> => {
      const query = filters ? this.buildQuery(filters as unknown as { [key: string]: unknown }) : undefined;
      const resp = await this.request<{ sandboxes: Sandbox[] }>(
        'GET',
        '/v1/sandboxes',
        undefined,
        query,
      );
      return resp.sandboxes;
    },

    /** Destroy a sandbox. Returns void on 202 Accepted. */
    destroy: async (id: string): Promise<void> => {
      await this.requestRaw('DELETE', `/v1/sandboxes/${id}`);
    },

    /** Restart a sandbox. Returns void on 202 Accepted. */
    restart: async (id: string): Promise<void> => {
      await this.requestRaw('POST', `/v1/sandboxes/${id}/restart`);
    },

    /**
     * Push files to a sandbox.
     * The control plane returns a 307 redirect to the agent's direct endpoint.
     * This method follows the redirect and sends the same body to the agent.
     */
    pushFiles: async (
      id: string,
      files: Array<{ path: string; content: string; delete?: boolean }>,
    ): Promise<FilePushResponse> => {
      const body = JSON.stringify({ files });
      const url = `${this.baseUrl}/v1/sandboxes/${id}/files`;

      // Send with redirect: 'manual' to intercept the 307
      const response = await fetch(url, {
        method: 'POST',
        headers: {
          'Authorization': `Bearer ${this.apiKey}`,
          'Content-Type': 'application/json',
        },
        body,
        redirect: 'manual',
      });

      if (response.status === 307) {
        const location = response.headers.get('Location');
        if (!location) {
          throw new Error('307 redirect missing Location header');
        }

        // Follow redirect to agent endpoint with same body
        const agentResponse = await fetch(location, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body,
        });

        if (!agentResponse.ok) {
          await this.handleError(agentResponse);
        }

        return agentResponse.json() as Promise<FilePushResponse>;
      }

      // Direct response (no redirect)
      if (!response.ok) {
        await this.handleError(response);
      }

      return response.json() as Promise<FilePushResponse>;
    },

    /** Get sandbox logs. Returns plain text. */
    logs: async (
      id: string,
      opts?: { tail?: number; follow?: boolean },
    ): Promise<string> => {
      const query = opts ? this.buildQuery(opts as unknown as { [key: string]: unknown }) : undefined;
      const url = this.buildUrl(`/v1/sandboxes/${id}/logs`, query);

      const response = await fetch(url, {
        method: 'GET',
        headers: {
          'Authorization': `Bearer ${this.apiKey}`,
        },
      });

      if (!response.ok) {
        await this.handleError(response);
      }

      return response.text();
    },
  };

  // ── Nodes ──────────────────────────────────────────────────

  nodes = {
    /** List all registered nodes. */
    list: async (): Promise<Node[]> => {
      const resp = await this.request<{ nodes: Node[] }>('GET', '/v1/nodes');
      return resp.nodes;
    },
  };

  // ── Routes ─────────────────────────────────────────────────

  routes = {
    /** List active routes (debug/ops). */
    list: async (): Promise<Route[]> => {
      const resp = await this.request<{ routes: Route[] }>('GET', '/v1/routes');
      return resp.routes;
    },
  };

  // ── Healthcheck ────────────────────────────────────────────

  /** Check control plane health. */
  async healthcheck(): Promise<{
    status: string;
    postgres: string;
    uptime_seconds: number;
  }> {
    return this.request('GET', '/v1/healthz');
  }

  // ── Private Helpers ────────────────────────────────────────

  /** Make a JSON request and parse the response body. */
  private async request<T>(
    method: string,
    path: string,
    body?: unknown,
    query?: string,
  ): Promise<T> {
    const url = this.buildUrl(path, query);

    const headers: Record<string, string> = {
      'Authorization': `Bearer ${this.apiKey}`,
    };

    const init: RequestInit = { method, headers };

    if (body !== undefined) {
      headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(body);
    }

    const response = await fetch(url, init);

    if (!response.ok) {
      await this.handleError(response);
    }

    return response.json() as Promise<T>;
  }

  /** Make a request that returns no body (202, 204). */
  private async requestRaw(
    method: string,
    path: string,
    body?: unknown,
  ): Promise<void> {
    const url = this.buildUrl(path);

    const headers: Record<string, string> = {
      'Authorization': `Bearer ${this.apiKey}`,
    };

    const init: RequestInit = { method, headers };

    if (body !== undefined) {
      headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(body);
    }

    const response = await fetch(url, init);

    if (!response.ok && response.status !== 202) {
      await this.handleError(response);
    }
  }

  /** Build full URL from path and optional query string. */
  private buildUrl(path: string, query?: string): string {
    const base = `${this.baseUrl}${path}`;
    return query ? `${base}?${query}` : base;
  }

  /** Build query string from object, omitting undefined values. */
  private buildQuery(params: { [key: string]: unknown }): string | undefined {
    const parts: string[] = [];
    for (const [key, value] of Object.entries(params)) {
      if (value !== undefined) {
        parts.push(`${encodeURIComponent(key)}=${encodeURIComponent(String(value))}`);
      }
    }
    return parts.length > 0 ? parts.join('&') : undefined;
  }

  /** Parse error response and throw typed error. */
  private async handleError(response: Response): Promise<never> {
    let problem: ForgeErrorResponse;

    try {
      problem = (await response.json()) as ForgeErrorResponse;
    } catch {
      problem = {
        type: 'urn:forge:error:unknown',
        title: response.statusText || 'Unknown error',
        status: response.status,
      };
    }

    switch (response.status) {
      case 404:
        throw new ForgeNotFoundError(problem);
      case 409:
        throw new ForgeConflictError(problem);
      case 503:
        throw new ForgeServiceError(problem);
      default:
        throw new ForgeError(problem);
    }
  }
}
