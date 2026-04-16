// Stub client -- implementation in GREEN phase
import type {
  Sandbox,
  SandboxCreate,
  SandboxListFilters,
  FilePushResponse,
  Node,
  Route,
} from './types.js';

export interface ForgeClientConfig {
  baseUrl: string;
  apiKey: string;
}

export class ForgeClient {
  constructor(_config: ForgeClientConfig) {
    // Stub -- not implemented
    throw new Error('ForgeClient not implemented');
  }

  sandboxes = {
    create: async (_req: SandboxCreate): Promise<Sandbox> => {
      throw new Error('not implemented');
    },
    get: async (_idOrApp: string): Promise<Sandbox> => {
      throw new Error('not implemented');
    },
    list: async (_filters?: SandboxListFilters): Promise<Sandbox[]> => {
      throw new Error('not implemented');
    },
    destroy: async (_id: string): Promise<void> => {
      throw new Error('not implemented');
    },
    restart: async (_id: string): Promise<void> => {
      throw new Error('not implemented');
    },
    pushFiles: async (
      _id: string,
      _files: Array<{ path: string; content: string; delete?: boolean }>,
    ): Promise<FilePushResponse> => {
      throw new Error('not implemented');
    },
    logs: async (
      _id: string,
      _opts?: { tail?: number; follow?: boolean },
    ): Promise<string> => {
      throw new Error('not implemented');
    },
  };

  nodes = {
    list: async (): Promise<Node[]> => {
      throw new Error('not implemented');
    },
  };

  routes = {
    list: async (): Promise<Route[]> => {
      throw new Error('not implemented');
    },
  };

  async healthcheck(): Promise<{
    status: string;
    postgres: string;
    uptime_seconds: number;
  }> {
    throw new Error('not implemented');
  }
}
