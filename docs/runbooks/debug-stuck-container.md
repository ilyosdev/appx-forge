# Runbook: Debug a Stuck Container

## When to Use

A sandbox appears stuck: its state is `starting` for more than 60 seconds, or it is `running` but the preview URL returns 502/504 errors.

## Symptoms

| Symptom | Likely Cause |
|---------|-------------|
| State = `starting` for >60s | Container not starting, or agent not reporting back |
| State = `running` but URL returns 502 | Container is running but Metro is not serving, or Caddy route is wrong |
| State = `running` but URL returns 504 | Container is running but Metro is hung (no response within timeout) |
| State = `running`, logs show nothing | Metro process exited silently, Docker still reports container as running |

## Diagnosis

### Step 1: Inspect the sandbox

```bash
forge sandbox inspect <SANDBOX_ID>
```

Note the key fields:
- `state` -- current reported state
- `node_id` -- which node hosts this sandbox
- `container_id` -- Docker container ID (if started)
- `host_port` -- port on the node where Metro should be listening
- `app_name` -- used as container name prefix (`forge-<app_name>`)

### Step 2: SSH to the node

```bash
ssh root@<NODE_IP>
```

### Step 3: Check Docker container state

```bash
# List all Forge containers
docker ps -a | grep forge-<APP_NAME>

# Detailed container info
docker inspect forge-<APP_NAME> --format '{{.State.Status}} (running={{.State.Running}}, exitcode={{.State.ExitCode}}, oom={{.State.OOMKilled}})'
```

Possible outcomes:

| Docker Status | Meaning | Action |
|---------------|---------|--------|
| `running` | Container is up but Metro may be broken | Go to Step 4 |
| `exited` | Container crashed, agent may not have reported | Go to Step 5 |
| `created` | Container was created but never started | Go to Step 5 |
| Not found | Container doesn't exist | Go to Step 6 |

### Step 4: Check container logs (if running)

```bash
docker logs forge-<APP_NAME> --tail=100
```

Look for:
- **Metro startup messages**: "Metro has started" or "Starting Metro on port 8081"
- **Error messages**: Stack traces, module not found, port binding failures
- **Silence**: If no output at all, Metro may have hung during startup

Check if Metro is actually listening:

```bash
# From the node, test the container port directly
curl -s -o /dev/null -w "%{http_code}" http://localhost:<HOST_PORT>/status
# Expected: 200 if Metro is healthy
```

If Metro is not responding:

```bash
# Check what process is running inside the container
docker exec forge-<APP_NAME> ps aux

# Check Metro's internal state
docker exec forge-<APP_NAME> curl -s http://localhost:8081/status 2>/dev/null || echo "Metro not responding inside container"
```

### Step 5: Check agent logs

```bash
journalctl -u forge-agent -n 100 --no-pager
```

Look for:
- `start_sandbox` command received but error during execution
- Docker API errors (image pull failures, resource conflicts)
- Heartbeat timeouts (agent disconnected from control plane)

### Step 6: Force recovery

If the container is stuck and cannot be recovered:

```bash
# Force remove the container on the node
docker rm -f forge-<APP_NAME>

# Then restart via the control plane
forge sandbox restart <SANDBOX_ID>
```

This forces the control plane to issue a new `start_sandbox` command to the agent.

If restart doesn't work:

```bash
# Destroy and let appx-api recreate
forge sandbox destroy <SANDBOX_ID>
```

## Root Cause Patterns

### Metro crash loop

**Indicator**: Container restarts rapidly, logs show syntax errors or missing modules.

**Fix**: The generated code has errors. This is an appx-api issue, not a Forge issue. The code validation pipeline should catch these before push.

```bash
# Check what code is in the bind mount
ls -la /var/lib/forge/sandboxes/<APP_NAME>/code/
cat /var/lib/forge/sandboxes/<APP_NAME>/code/App.tsx | head -50
```

### Bind mount permission issue

**Indicator**: Metro starts but cannot read files, or "EACCES" errors in logs.

**Fix**: The bind mount directory should be owned by UID 1000 (the user inside the container).

```bash
# Check permissions
ls -la /var/lib/forge/sandboxes/<APP_NAME>/

# Fix permissions
chown -R 1000:1000 /var/lib/forge/sandboxes/<APP_NAME>/code/

# Restart
forge sandbox restart <SANDBOX_ID>
```

### Port conflict

**Indicator**: Container cannot bind to the assigned host port. `EADDRINUSE` in Docker logs.

**Fix**: Another container or process is using the port.

```bash
# Check what's using the port
ss -tlnp | grep <HOST_PORT>

# If it's a zombie Forge container
docker rm -f <CONFLICTING_CONTAINER>

# Restart the agent to reset the port allocator
systemctl restart forge-agent
```

### OOM kill

**Indicator**: `docker inspect` shows `OOMKilled: true`, or `dmesg | grep -i oom` shows kills.

**Fix**: Container memory limit is too low for the app, or the node is overloaded.

```bash
# Check container memory limit
docker inspect forge-<APP_NAME> --format '{{.HostConfig.Memory}}'

# Check node memory pressure
free -h
docker stats --no-stream
```

### Caddy route mismatch

**Indicator**: Container is running and Metro responds locally, but the public URL returns 502.

**Fix**: The Caddy route points to the wrong node/port, or the route is missing.

```bash
# Check routes in Forge
forge routes list | grep <APP_NAME>

# Verify Caddy has the route
forge routes verify

# If route is missing or wrong, restart the sandbox to trigger route re-add
forge sandbox restart <SANDBOX_ID>
```

## Recovery Checklist

- [ ] Identified the sandbox: `forge sandbox inspect <ID>`
- [ ] Checked Docker state on the node: `docker ps -a | grep forge-<APP_NAME>`
- [ ] Reviewed container logs: `docker logs forge-<APP_NAME> --tail=100`
- [ ] Reviewed agent logs: `journalctl -u forge-agent -n 100`
- [ ] Attempted restart: `forge sandbox restart <ID>`
- [ ] If restart failed, force removed: `docker rm -f forge-<APP_NAME>` + restart
- [ ] If still broken, destroyed and will let appx-api recreate
- [ ] Root cause documented for future prevention
