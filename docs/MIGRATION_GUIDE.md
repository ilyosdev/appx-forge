# Migration Guide: Railover to Forge in appx-api

This document describes exactly what to change in the **appx-api** codebase (text2design repo) to replace RailoverService with ForgeClient from `@appx/forge-sdk`.

## Overview

Replace three backend services with a single ForgeClient SDK wrapper:

| Remove | Replace With |
|--------|-------------|
| `RailoverService` (HTTP client to CapRover fork) | `ForgeService` (NestJS wrapper around ForgeClient) |
| `ContainerReconcilerService` (periodic state sync) | Nothing -- Forge control plane is the source of truth |
| `ContainerCircuitBreakerService` (failure protection) | Nothing -- Forge handles retries and failure states |

## Step 1: Install the SDK

```bash
cd backend
npm install @appx/forge-sdk
```

The SDK provides:
- `ForgeClient` -- HTTP client for the Forge control plane
- `Sandbox`, `SandboxCreate`, `SandboxListFilters` -- typed request/response interfaces
- `ForgeError`, `ForgeNotFoundError`, `ForgeConflictError` -- typed error classes

## Step 2: Add Environment Variables

Add to `backend/.env`:

```env
FORGE_CONTROL_URL=https://forge.internal.myappx.live
FORGE_API_KEY=<your-api-key>
```

Create `backend/src/config/forge.config.ts`:

```typescript
import { registerAs } from '@nestjs/config';

export default registerAs('forge', () => ({
  controlUrl: process.env.FORGE_CONTROL_URL || 'http://localhost:3080',
  apiKey: process.env.FORGE_API_KEY || '',
}));
```

Add to the config imports in `backend/src/config/index.ts`:

```typescript
export { default as forgeConfig } from './forge.config';
```

Add to `ConfigModule.forRoot({ load: [...] })` in `app.module.ts`.

## Step 3: Create ForgeService (NestJS Wrapper)

Create `backend/src/modules/deployment/forge.service.ts`:

```typescript
import { Injectable, Logger, OnModuleInit } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import { ForgeClient } from '@appx/forge-sdk';
import type { Sandbox, SandboxCreate, FilePushResponse } from '@appx/forge-sdk';

@Injectable()
export class ForgeService implements OnModuleInit {
  private readonly logger = new Logger(ForgeService.name);
  private client: ForgeClient;

  constructor(private readonly config: ConfigService) {}

  onModuleInit() {
    const controlUrl = this.config.get<string>('forge.controlUrl');
    const apiKey = this.config.get<string>('forge.apiKey');

    if (!controlUrl || !apiKey) {
      this.logger.warn('Forge config missing -- ForgeService will not function');
      return;
    }

    this.client = new ForgeClient({
      baseUrl: controlUrl,
      apiKey,
    });

    this.logger.log(`ForgeService initialized: ${controlUrl}`);
  }

  /**
   * Create a sandbox for a user's project.
   * Replaces: RailoverService.createApp()
   */
  async createApp(
    appName: string,
    userId: string,
    image: string,
    resources?: { cpuCores?: number; memoryMb?: number },
  ): Promise<Sandbox> {
    return this.client.sandboxes.create({
      app_name: appName,
      user_id: userId,
      image,
      resources: resources
        ? { cpu_cores: resources.cpuCores, memory_mb: resources.memoryMb }
        : undefined,
    });
  }

  /**
   * Get sandbox status by ID.
   * Replaces: RailoverService.getAppStatus()
   */
  async getAppStatus(sandboxId: string): Promise<Sandbox> {
    return this.client.sandboxes.get(sandboxId);
  }

  /**
   * Get sandbox by app name.
   * New capability -- Railover required looking up by container ID.
   */
  async getAppByName(appName: string): Promise<Sandbox> {
    return this.client.sandboxes.get(`app:${appName}`);
  }

  /**
   * Push files to a sandbox's bind mount.
   * Replaces: RailoverService.pushCode() / ToolboxClientService file push
   *
   * The SDK handles the 307 redirect to the agent automatically.
   */
  async pushCode(
    sandboxId: string,
    files: Array<{ path: string; content: string }>,
  ): Promise<FilePushResponse> {
    return this.client.sandboxes.pushFiles(sandboxId, files);
  }

  /**
   * Destroy a sandbox and its container.
   * Replaces: RailoverService.deleteApp()
   */
  async deleteApp(sandboxId: string): Promise<void> {
    return this.client.sandboxes.destroy(sandboxId);
  }

  /**
   * Force restart a sandbox.
   * Replaces: manual docker restart via Railover
   */
  async restartApp(sandboxId: string): Promise<void> {
    return this.client.sandboxes.restart(sandboxId);
  }

  /**
   * Get sandbox logs.
   * Replaces: SSH + docker logs
   */
  async getLogs(sandboxId: string, tail?: number): Promise<string> {
    return this.client.sandboxes.logs(sandboxId, { tail });
  }

  /**
   * List sandboxes for a user.
   * New capability -- Railover didn't support listing.
   */
  async listUserSandboxes(userId: string): Promise<Sandbox[]> {
    return this.client.sandboxes.list({ user_id: userId });
  }

  /**
   * Health check.
   * Replaces: Railover health check (which was unreliable due to 60s restart loop)
   */
  async healthcheck(): Promise<{ status: string }> {
    return this.client.healthcheck();
  }
}
```

## Step 4: Replace RailoverService References

Every file in appx-api that imports `RailoverService` needs updating.

### deployment.module.ts

```diff
- import { RailoverService } from './railover.service';
- import { ContainerReconcilerService } from './container-reconciler.service';
- import { ContainerCircuitBreakerService } from './container-circuit-breaker.service';
+ import { ForgeService } from './forge.service';

@Module({
  providers: [
    DeploymentService,
-   RailoverService,
-   ContainerReconcilerService,
-   ContainerCircuitBreakerService,
+   ForgeService,
    ContainerPoolService,
    ContainerHealthService,
  ],
  exports: [
    DeploymentService,
-   RailoverService,
+   ForgeService,
  ],
})
```

### deployment.service.ts

```diff
- import { RailoverService } from './railover.service';
+ import { ForgeService } from './forge.service';

@Injectable()
export class DeploymentService {
  constructor(
-   private readonly railover: RailoverService,
+   private readonly forge: ForgeService,
  ) {}

  async provisionContainer(projectId: string, appName: string, userId: string) {
-   const result = await this.railover.createApp(appName);
-   // Railover returns: { appName, status }
+   const sandbox = await this.forge.createApp(appName, userId, 'appx/sandbox:v1');
+   // Forge returns: Sandbox with id, url, state, etc.
+   return sandbox;
  }

  async destroyContainer(containerId: string) {
-   await this.railover.deleteApp(containerId);
+   await this.forge.deleteApp(containerId);  // containerId is now sandboxId
  }

  async getContainerStatus(containerId: string) {
-   const status = await this.railover.getAppStatus(containerId);
-   return mapRailoverStatus(status);
+   const sandbox = await this.forge.getAppStatus(containerId);
+   return mapForgeState(sandbox.state);
  }
}
```

### container-pool.service.ts

```diff
- import { RailoverService } from './railover.service';
+ import { ForgeService } from './forge.service';

export class ContainerPoolService {
  constructor(
-   private readonly railover: RailoverService,
+   private readonly forge: ForgeService,
  ) {}

  async provisionWarmContainer() {
    const appName = this.generateAppName();
-   await this.railover.createApp(appName);
+   const sandbox = await this.forge.createApp(appName, 'pool', 'appx/sandbox:v1');
+   // Store sandbox.id instead of appName for future operations
  }
}
```

### toolbox-client.service.ts (or wherever file push happens)

```diff
- import { RailoverService } from '../deployment/railover.service';
+ import { ForgeService } from '../deployment/forge.service';

export class ToolboxClientService {
  constructor(
-   private readonly railover: RailoverService,
+   private readonly forge: ForgeService,
  ) {}

  async pushFilesToContainer(containerId: string, files: Array<{ path: string; content: string }>) {
-   // Railover: POST to file server, needs app/ prefix
-   for (const file of files) {
-     await this.railover.pushCode(containerId, `app/${file.path}`, file.content);
-   }
+   // Forge: single call, SDK handles 307 redirect to agent
+   await this.forge.pushCode(containerId, files);
  }
}
```

### project.gateway.ts

```diff
- import { RailoverService } from '../deployment/railover.service';
+ import { ForgeService } from '../deployment/forge.service';

export class ProjectGateway {
  constructor(
-   private readonly railover: RailoverService,
+   private readonly forge: ForgeService,
  ) {}

  // Update all Railover method calls similarly
}
```

## Step 5: Delete These Files

These files are no longer needed -- their functionality is handled by the Forge control plane:

```bash
cd backend/src/modules/deployment

# RailoverService -- replaced by ForgeService
rm railover.service.ts

# ContainerReconcilerService -- Forge control plane handles state
# The old reconciler existed because Railover/Swarm had drift issues.
# Forge has no reconciliation loop by design (event-driven only).
rm container-reconciler.service.ts

# ContainerCircuitBreakerService -- Forge handles retries with backoff
# The old circuit breaker existed because Railover would 502 during its
# 60s restart loop. Forge has no restart loop.
rm container-circuit-breaker.service.ts
```

Also remove any barrel exports (`index.ts`) that reference these files.

## Step 6: Update Container State Mapping

Update `backend/src/common/types/container-state.types.ts` to map Forge sandbox states to the existing appx-api container states:

```typescript
/**
 * Maps Forge sandbox states to appx-api container states.
 * Forge uses: pending | starting | running | restarting | stopped | destroying | destroyed | failed
 * appx-api uses: pending | provisioning | running | sleeping | destroying | destroyed | error
 */
export const FORGE_STATE_MAP: Record<string, string> = {
  pending: 'pending',
  starting: 'provisioning',
  running: 'running',
  restarting: 'running',       // Container is restarting -- still "running" from user's perspective
  stopped: 'sleeping',
  destroying: 'destroying',
  destroyed: 'destroyed',
  failed: 'error',
};

/**
 * Convert a Forge sandbox state to an appx-api container state.
 */
export function mapForgeState(forgeState: string): string {
  return FORGE_STATE_MAP[forgeState] ?? 'error';
}
```

## Step 7: Update Container URL Generation

Forge returns the full URL on sandbox creation:

```typescript
// Old (Railover): construct URL from app name
const url = `https://${appName}.myappx.live`;

// New (Forge): URL is in the sandbox response
const sandbox = await forge.createApp(appName, userId, image);
const url = sandbox.url;  // "https://<app_name>.myappx.live"
```

## Step 8: Integration Test Checklist

After migration, verify end-to-end:

- [ ] **Create sandbox**: `ForgeService.createApp()` returns a Sandbox object with state `pending`
- [ ] **Push files**: `ForgeService.pushCode()` follows the 307 redirect and writes files
- [ ] **Metro rebuilds**: After file push, the sandbox URL serves updated content within 2-5 seconds
- [ ] **Get status**: `ForgeService.getAppStatus()` returns current state and URL
- [ ] **Destroy sandbox**: `ForgeService.deleteApp()` returns successfully
- [ ] **Container pool**: Pool service creates warm containers using ForgeService
- [ ] **Chat/generation flow**: AI generates code, pushes via ForgeService, preview shows result
- [ ] **State mapping**: `mapForgeState()` correctly maps all states for the frontend
- [ ] **Error handling**: ForgeNotFoundError, ForgeConflictError are caught and mapped to NestJS exceptions
- [ ] **Health check**: `ForgeService.healthcheck()` returns status from control plane

## Summary of Changes

| Category | Files Changed | Description |
|----------|---------------|-------------|
| New | `forge.config.ts` | Forge config (registerAs) |
| New | `forge.service.ts` | NestJS wrapper around ForgeClient |
| Modified | `deployment.module.ts` | Swap providers |
| Modified | `deployment.service.ts` | Use ForgeService |
| Modified | `container-pool.service.ts` | Use ForgeService |
| Modified | `toolbox-client.service.ts` | Use ForgeService.pushCode() |
| Modified | `project.gateway.ts` | Use ForgeService |
| Modified | `container-state.types.ts` | Add Forge state mapping |
| Deleted | `railover.service.ts` | Replaced by ForgeService |
| Deleted | `container-reconciler.service.ts` | No longer needed |
| Deleted | `container-circuit-breaker.service.ts` | No longer needed |

## Key Differences from Railover

| Aspect | Railover | Forge |
|--------|----------|-------|
| Container creation | HTTP to CapRover API, Swarm service created | HTTP to control plane, plain `docker run` on agent |
| File push | HTTP to file-server on Server 2, `app/` path prefix required | SDK handles 307 redirect to agent, no prefix needed |
| State source of truth | DB in appx-api (drift-prone) | Postgres in Forge control plane (event-driven) |
| Crash recovery | Manual circuit breaker + reconciler in appx-api | Auto-restart with backoff in control plane |
| Container health | Polled every 60s with frequent false positives | Docker events (immediate), heartbeat every 15s |
| URL routing | Traefik with Docker labels, WebSocket drops on config reload | Caddy with Admin API, 500ms debounce batching |
| Multi-node | Docker Swarm with 60s restart bug | Plain Docker + Tailscale mesh, bin-packing scheduler |
