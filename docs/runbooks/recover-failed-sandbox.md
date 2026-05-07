# Runbook: Recover a Failed Sandbox

## When to Use

A sandbox has entered the `failed` state, meaning it crashed more than 3 times and the auto-restart backoff was exhausted. The user's preview is down.

## Identify

List all failed sandboxes:

```bash
forge sandbox list --state=failed
```

Example output:

```
ID                                   APP_NAME              STATE    NODE         FAILURES
a1b2c3d4-...                         user-cool-app-abc     failed   node1        4
```

## Diagnose

### Step 1: Inspect the sandbox

```bash
forge sandbox inspect <SANDBOX_ID>
```

Key fields to check:
- `failure_count` -- how many times it crashed
- `state` -- should be `failed`
- `node_id` -- which node it was on
- `updated_at` -- when the last crash happened
- `metadata` -- any crash context

### Step 2: Check sandbox logs

```bash
forge sandbox logs <SANDBOX_ID> --tail=200
```

Common crash patterns:
- **Metro crash loop**: Look for `ENOSPC` (inotify exhausted), `ENOMEM` (OOM), or syntax errors in generated code
- **Missing dependencies**: `Cannot find module '...'` -- sandbox image may be outdated
- **Port conflict**: `EADDRINUSE` -- another container grabbed the port

### Step 3: Check events timeline

```bash
forge events --sandbox=<SANDBOX_ID>
```

This shows the full event history: created, started, exited, restarted, failed. Look for patterns like rapid crash loops (all exits within seconds of start).

### Step 4: Check the node

```bash
forge node list
```

Is the node healthy? If the node itself is unhealthy, the problem is infrastructure, not the sandbox.

```bash
# Check node resource usage
forge sandbox list --node=<NODE_ID>
```

Is the node overloaded (too many sandboxes for available RAM)?

## Recovery Options

### Option A: Restart the sandbox

If the root cause was transient (temporary resource pressure, brief network issue):

```bash
forge sandbox restart <SANDBOX_ID>
```

This resets the failure count and starts the container fresh. Monitor:

```bash
forge sandbox inspect <SANDBOX_ID>
# Wait 10s, check state transitions to 'running'
```

### Option B: Destroy and recreate

If the sandbox is corrupted (bad code in bind mount, broken container state):

```bash
# Destroy the failed sandbox
forge sandbox destroy <SANDBOX_ID>

# Recreate (typically done by appx-api, but can be done manually for testing)
# forge sandbox create --app=<APP_NAME> --user-id=<USER_ID> --image=appx/sandbox:v1
```

The user's next preview request will trigger appx-api to create a new sandbox.

### Option C: SSH to node for deep inspection

If the above options don't reveal the cause:

```bash
# SSH to the node
ssh root@<NODE_IP>

# Check Docker container state
docker ps -a | grep forge-<APP_NAME>

# Get container logs directly
docker logs forge-<APP_NAME> --tail=100

# Check container resource usage
docker stats forge-<APP_NAME> --no-stream

# Check bind mount contents
ls -la /var/lib/forge/sandboxes/<APP_NAME>/code/

# Check if seccomp is blocking something
journalctl -k | grep audit | tail -20
```

## Escalation

If the node itself is unhealthy:

```bash
# 1. Drain the node to stop new scheduling
forge node drain <NODE_ID>

# 2. Check agent status
ssh root@<NODE_IP> 'systemctl status forge-agent'
ssh root@<NODE_IP> 'journalctl -u forge-agent -n 100 --no-pager'

# 3. Check Docker daemon
ssh root@<NODE_IP> 'docker info'

# 4. Check system resources
ssh root@<NODE_IP> 'free -h && df -h && uptime'

# 5. If recoverable, restart the agent
ssh root@<NODE_IP> 'systemctl restart forge-agent'

# 6. Remove drain once healthy
# (Not yet implemented -- re-register or restart agent to clear drain)
```

## Prevention

- **Monitor failure_count**: Set alerts when any sandbox has `failure_count > 0`
- **Monitor node capacity**: Alert when node `used_mb` exceeds 80% of `capacity_mb`
- **Check inotify limits**: Each Metro instance uses ~5000-6000 watches. At 80+ containers, ensure `fs.inotify.max_user_watches=524288`
- **Keep sandbox image updated**: Old images may have missing dependencies
- **Review logs proactively**: `forge sandbox list --state=running` and spot-check logs for warnings

## Common Root Causes

| Cause | Indicator | Fix |
|-------|-----------|-----|
| OOM kill | `docker inspect` shows OOMKilled=true | Increase container memory limit or reduce node load |
| Metro syntax error | Logs show parse errors in App.tsx | Fix the generated code (appx-api side) |
| inotify exhaustion | `ENOSPC` in logs | Verify `/proc/sys/fs/inotify/max_user_watches` is 524288 |
| Stale bind mount | Container starts but Metro serves old code | `docker rm -f` + restart |
| Image not found | `docker pull` fails | Push image to registry, or pre-pull on node |
| Port conflict | `EADDRINUSE` | Agent port allocator has a bug or port was not released. Restart agent. |
