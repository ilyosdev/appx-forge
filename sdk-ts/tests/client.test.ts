import nock from 'nock';
import { ForgeClient } from '../src/client';
import { ForgeNotFoundError, ForgeConflictError, ForgeServiceError } from '../src/errors';
import type { Sandbox, Node, Route } from '../src/types';

const BASE_URL = 'http://forge.test:8080';
const API_KEY = 'test-api-key-secret';

// ── Test Fixtures ──────────────────────────────────────────────

const mockSandbox: Sandbox = {
  id: '550e8400-e29b-41d4-a716-446655440000',
  app_name: 'my-cool-app',
  user_id: 'user-123',
  node_id: 'node-abc',
  container_id: 'ctr-xyz',
  image: 'appx/sandbox:v1',
  state: 'running',
  url: 'https://my-cool-app.myappx.live',
  resources: { cpu_cores: 0.5, memory_mb: 512 },
  host_port: 43210,
  created_at: '2026-04-15T10:00:00Z',
  updated_at: '2026-04-15T10:01:00Z',
  last_active_at: '2026-04-15T10:05:00Z',
  failure_count: 0,
  metadata: {},
};

const mockNode: Node = {
  id: 'node-uuid-1',
  hostname: 'node-1',
  tailscale_ip: '100.64.1.5',
  capacity_mb: 24000,
  used_mb: 8000,
  capacity_cpu: 4,
  status: 'healthy',
  last_seen_at: '2026-04-15T10:00:00Z',
  registered_at: '2026-04-10T08:00:00Z',
  agent_version: '0.1.0',
  running_sandboxes: 5,
};

const mockRoute: Route = {
  app_name: 'my-cool-app',
  sandbox_id: '550e8400-e29b-41d4-a716-446655440000',
  upstream: '100.64.1.5:43210',
  updated_at: '2026-04-15T10:01:00Z',
};

// ── Setup / Teardown ───────────────────────────────────────────

beforeEach(() => {
  nock.cleanAll();
});

afterAll(() => {
  nock.cleanAll();
  nock.restore();
});

// ── Constructor ────────────────────────────────────────────────

describe('ForgeClient constructor', () => {
  it('accepts baseUrl and apiKey config', () => {
    const client = new ForgeClient({ baseUrl: BASE_URL, apiKey: API_KEY });
    expect(client).toBeDefined();
    expect(client.sandboxes).toBeDefined();
    expect(client.nodes).toBeDefined();
    expect(client.routes).toBeDefined();
  });
});

// ── Sandboxes ──────────────────────────────────────────────────

describe('sandboxes.create', () => {
  it('sends POST /v1/sandboxes with Bearer token and returns Sandbox', async () => {
    const scope = nock(BASE_URL)
      .post('/v1/sandboxes', {
        app_name: 'my-cool-app',
        user_id: 'user-123',
        image: 'appx/sandbox:v1',
      })
      .matchHeader('Authorization', `Bearer ${API_KEY}`)
      .matchHeader('Content-Type', 'application/json')
      .reply(201, mockSandbox);

    const client = new ForgeClient({ baseUrl: BASE_URL, apiKey: API_KEY });
    const result = await client.sandboxes.create({
      app_name: 'my-cool-app',
      user_id: 'user-123',
      image: 'appx/sandbox:v1',
    });

    expect(result).toEqual(mockSandbox);
    scope.done();
  });

  it('sends optional fields when provided', async () => {
    const scope = nock(BASE_URL)
      .post('/v1/sandboxes', {
        app_name: 'my-app',
        user_id: 'u1',
        image: 'appx/sandbox:v1',
        resources: { cpu_cores: 1, memory_mb: 1024 },
        env: { NODE_ENV: 'production' },
        idle_timeout_seconds: 3600,
        metadata: { project_id: 'proj-1' },
      })
      .matchHeader('Authorization', `Bearer ${API_KEY}`)
      .reply(201, mockSandbox);

    const client = new ForgeClient({ baseUrl: BASE_URL, apiKey: API_KEY });
    await client.sandboxes.create({
      app_name: 'my-app',
      user_id: 'u1',
      image: 'appx/sandbox:v1',
      resources: { cpu_cores: 1, memory_mb: 1024 },
      env: { NODE_ENV: 'production' },
      idle_timeout_seconds: 3600,
      metadata: { project_id: 'proj-1' },
    });

    scope.done();
  });
});

describe('sandboxes.get', () => {
  it('sends GET /v1/sandboxes/{id} and returns Sandbox', async () => {
    const id = '550e8400-e29b-41d4-a716-446655440000';
    const scope = nock(BASE_URL)
      .get(`/v1/sandboxes/${id}`)
      .matchHeader('Authorization', `Bearer ${API_KEY}`)
      .reply(200, mockSandbox);

    const client = new ForgeClient({ baseUrl: BASE_URL, apiKey: API_KEY });
    const result = await client.sandboxes.get(id);

    expect(result).toEqual(mockSandbox);
    scope.done();
  });

  it('supports app_name lookup with app: prefix', async () => {
    const scope = nock(BASE_URL)
      .get('/v1/sandboxes/app:my-cool-app')
      .matchHeader('Authorization', `Bearer ${API_KEY}`)
      .reply(200, mockSandbox);

    const client = new ForgeClient({ baseUrl: BASE_URL, apiKey: API_KEY });
    const result = await client.sandboxes.get('app:my-cool-app');

    expect(result).toEqual(mockSandbox);
    scope.done();
  });
});

describe('sandboxes.list', () => {
  it('sends GET /v1/sandboxes and returns Sandbox array', async () => {
    const scope = nock(BASE_URL)
      .get('/v1/sandboxes')
      .matchHeader('Authorization', `Bearer ${API_KEY}`)
      .reply(200, { sandboxes: [mockSandbox] });

    const client = new ForgeClient({ baseUrl: BASE_URL, apiKey: API_KEY });
    const result = await client.sandboxes.list();

    expect(result).toEqual([mockSandbox]);
    scope.done();
  });

  it('passes query params for filters', async () => {
    const scope = nock(BASE_URL)
      .get('/v1/sandboxes')
      .query({ state: 'running', user_id: 'u1' })
      .matchHeader('Authorization', `Bearer ${API_KEY}`)
      .reply(200, { sandboxes: [mockSandbox] });

    const client = new ForgeClient({ baseUrl: BASE_URL, apiKey: API_KEY });
    const result = await client.sandboxes.list({ state: 'running', user_id: 'u1' });

    expect(result).toEqual([mockSandbox]);
    scope.done();
  });

  it('passes all filter options', async () => {
    const scope = nock(BASE_URL)
      .get('/v1/sandboxes')
      .query({ app_name: 'my-app', user_id: 'u1', state: 'running', node_id: 'n1', limit: '10' })
      .matchHeader('Authorization', `Bearer ${API_KEY}`)
      .reply(200, { sandboxes: [] });

    const client = new ForgeClient({ baseUrl: BASE_URL, apiKey: API_KEY });
    const result = await client.sandboxes.list({
      app_name: 'my-app',
      user_id: 'u1',
      state: 'running',
      node_id: 'n1',
      limit: 10,
    });

    expect(result).toEqual([]);
    scope.done();
  });
});

describe('sandboxes.destroy', () => {
  it('sends DELETE /v1/sandboxes/{id} and returns void on 202', async () => {
    const id = '550e8400-e29b-41d4-a716-446655440000';
    const scope = nock(BASE_URL)
      .delete(`/v1/sandboxes/${id}`)
      .matchHeader('Authorization', `Bearer ${API_KEY}`)
      .reply(202);

    const client = new ForgeClient({ baseUrl: BASE_URL, apiKey: API_KEY });
    const result = await client.sandboxes.destroy(id);

    expect(result).toBeUndefined();
    scope.done();
  });
});

describe('sandboxes.restart', () => {
  it('sends POST /v1/sandboxes/{id}/restart and returns void on 202', async () => {
    const id = '550e8400-e29b-41d4-a716-446655440000';
    const scope = nock(BASE_URL)
      .post(`/v1/sandboxes/${id}/restart`)
      .matchHeader('Authorization', `Bearer ${API_KEY}`)
      .reply(202);

    const client = new ForgeClient({ baseUrl: BASE_URL, apiKey: API_KEY });
    const result = await client.sandboxes.restart(id);

    expect(result).toBeUndefined();
    scope.done();
  });
});

describe('sandboxes.pushFiles', () => {
  it('follows 307 redirect to agent endpoint and returns FilePushResponse', async () => {
    const id = '550e8400-e29b-41d4-a716-446655440000';
    const agentUrl = 'http://100.64.1.5:8090';
    const signedPath = `/sandboxes/${id}/files?token=signed-hmac-token&expires=9999999999`;

    const files = [
      { path: 'App.tsx', content: 'Y29uc29sZS5sb2coImhlbGxvIik=' },
      { path: 'package.json', content: 'eyJuYW1lIjoiYXBwIn0=' },
    ];

    // Control plane returns 307 redirect
    const controlScope = nock(BASE_URL)
      .post(`/v1/sandboxes/${id}/files`, { files })
      .matchHeader('Authorization', `Bearer ${API_KEY}`)
      .reply(307, '', {
        Location: `${agentUrl}${signedPath}`,
      });

    // Agent endpoint returns success
    const agentScope = nock(agentUrl)
      .post(signedPath, { files })
      .reply(200, {
        written: ['App.tsx', 'package.json'],
        failed: [],
      });

    const client = new ForgeClient({ baseUrl: BASE_URL, apiKey: API_KEY });
    const result = await client.sandboxes.pushFiles(id, files);

    expect(result).toEqual({
      written: ['App.tsx', 'package.json'],
      failed: [],
    });
    controlScope.done();
    agentScope.done();
  });
});

describe('sandboxes.logs', () => {
  it('sends GET /v1/sandboxes/{id}/logs and returns log text', async () => {
    const id = '550e8400-e29b-41d4-a716-446655440000';
    const logText = 'Starting Metro bundler...\nReady on port 8081\n';

    const scope = nock(BASE_URL)
      .get(`/v1/sandboxes/${id}/logs`)
      .matchHeader('Authorization', `Bearer ${API_KEY}`)
      .reply(200, logText);

    const client = new ForgeClient({ baseUrl: BASE_URL, apiKey: API_KEY });
    const result = await client.sandboxes.logs(id);

    expect(result).toBe(logText);
    scope.done();
  });

  it('passes tail query param', async () => {
    const id = '550e8400-e29b-41d4-a716-446655440000';
    const logText = 'last 50 lines...';

    const scope = nock(BASE_URL)
      .get(`/v1/sandboxes/${id}/logs`)
      .query({ tail: '50' })
      .matchHeader('Authorization', `Bearer ${API_KEY}`)
      .reply(200, logText);

    const client = new ForgeClient({ baseUrl: BASE_URL, apiKey: API_KEY });
    const result = await client.sandboxes.logs(id, { tail: 50 });

    expect(result).toBe(logText);
    scope.done();
  });
});

// ── Authorization Header ───────────────────────────────────────

describe('Authorization header', () => {
  it('sends Bearer token on every request', async () => {
    const scope = nock(BASE_URL)
      .get('/v1/healthz')
      .matchHeader('Authorization', `Bearer ${API_KEY}`)
      .reply(200, { status: 'ok', postgres: 'ok', uptime_seconds: 120 });

    const client = new ForgeClient({ baseUrl: BASE_URL, apiKey: API_KEY });
    await client.healthcheck();

    scope.done();
  });
});

// ── Error Handling ─────────────────────────────────────────────

describe('Error handling', () => {
  it('throws ForgeNotFoundError on 404', async () => {
    const problem = {
      type: 'urn:forge:error:not-found',
      title: 'Sandbox not found',
      status: 404,
      detail: 'No sandbox with id missing-id',
    };

    nock(BASE_URL)
      .get('/v1/sandboxes/missing-id')
      .reply(404, problem, { 'Content-Type': 'application/problem+json' });

    const client = new ForgeClient({ baseUrl: BASE_URL, apiKey: API_KEY });

    await expect(client.sandboxes.get('missing-id')).rejects.toThrow(ForgeNotFoundError);
    await expect(
      (async () => {
        try {
          await new ForgeClient({ baseUrl: BASE_URL, apiKey: API_KEY }).sandboxes.get('missing-id');
        } catch (e) {
          // Re-mock for second call
          expect((e as ForgeNotFoundError).problem.status).toBe(404);
          expect((e as ForgeNotFoundError).problem.detail).toBe('No sandbox with id missing-id');
          throw e;
        }
      })(),
    ).rejects.toThrow(ForgeNotFoundError);
  });

  it('throws ForgeConflictError on 409', async () => {
    const problem = {
      type: 'urn:forge:error:conflict',
      title: 'app_name already exists',
      status: 409,
    };

    nock(BASE_URL)
      .post('/v1/sandboxes')
      .reply(409, problem, { 'Content-Type': 'application/problem+json' });

    const client = new ForgeClient({ baseUrl: BASE_URL, apiKey: API_KEY });

    await expect(
      client.sandboxes.create({
        app_name: 'existing-app',
        user_id: 'u1',
        image: 'appx/sandbox:v1',
      }),
    ).rejects.toThrow(ForgeConflictError);
  });

  it('throws ForgeServiceError on 503', async () => {
    const problem = {
      type: 'urn:forge:error:service-unavailable',
      title: 'No nodes available',
      status: 503,
    };

    nock(BASE_URL)
      .post('/v1/sandboxes')
      .reply(503, problem, { 'Content-Type': 'application/problem+json' });

    const client = new ForgeClient({ baseUrl: BASE_URL, apiKey: API_KEY });

    await expect(
      client.sandboxes.create({
        app_name: 'new-app',
        user_id: 'u1',
        image: 'appx/sandbox:v1',
      }),
    ).rejects.toThrow(ForgeServiceError);
  });

  it('throws ForgeError for other error status codes', async () => {
    const problem = {
      type: 'urn:forge:error:bad-request',
      title: 'Invalid input',
      status: 400,
      detail: 'app_name must match pattern',
    };

    nock(BASE_URL)
      .post('/v1/sandboxes')
      .reply(400, problem, { 'Content-Type': 'application/problem+json' });

    const client = new ForgeClient({ baseUrl: BASE_URL, apiKey: API_KEY });
    const { ForgeError } = await import('../src/errors');

    await expect(
      client.sandboxes.create({
        app_name: 'INVALID',
        user_id: 'u1',
        image: 'appx/sandbox:v1',
      }),
    ).rejects.toThrow(ForgeError);
  });
});

// ── Nodes ──────────────────────────────────────────────────────

describe('nodes.list', () => {
  it('sends GET /v1/nodes and returns Node array', async () => {
    const scope = nock(BASE_URL)
      .get('/v1/nodes')
      .matchHeader('Authorization', `Bearer ${API_KEY}`)
      .reply(200, { nodes: [mockNode] });

    const client = new ForgeClient({ baseUrl: BASE_URL, apiKey: API_KEY });
    const result = await client.nodes.list();

    expect(result).toEqual([mockNode]);
    scope.done();
  });
});

// ── Routes ─────────────────────────────────────────────────────

describe('routes.list', () => {
  it('sends GET /v1/routes and returns Route array', async () => {
    const scope = nock(BASE_URL)
      .get('/v1/routes')
      .matchHeader('Authorization', `Bearer ${API_KEY}`)
      .reply(200, { routes: [mockRoute] });

    const client = new ForgeClient({ baseUrl: BASE_URL, apiKey: API_KEY });
    const result = await client.routes.list();

    expect(result).toEqual([mockRoute]);
    scope.done();
  });
});

// ── Healthcheck ────────────────────────────────────────────────

describe('healthcheck', () => {
  it('sends GET /v1/healthz and returns health status', async () => {
    const health = { status: 'ok', postgres: 'ok', uptime_seconds: 3600 };

    const scope = nock(BASE_URL)
      .get('/v1/healthz')
      .matchHeader('Authorization', `Bearer ${API_KEY}`)
      .reply(200, health);

    const client = new ForgeClient({ baseUrl: BASE_URL, apiKey: API_KEY });
    const result = await client.healthcheck();

    expect(result).toEqual(health);
    scope.done();
  });
});
