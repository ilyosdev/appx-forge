# Runbook: Add a New Node to the Forge Fleet

## Prerequisites

- [ ] Contabo VDS ordered and SSH root access confirmed
- [ ] Node has at least 8GB RAM (recommended 24GB for ~80 containers)
- [ ] Node runs Ubuntu 22.04 or 24.04 (Debian 12 also supported)
- [ ] Tailscale auth key generated (reusable, ephemeral) from [Tailscale admin](https://login.tailscale.com/admin/settings/keys)
- [ ] `forge-agent` binary built for linux/amd64 and available locally
- [ ] Ansible installed on your workstation (`pip install ansible`)
- [ ] Control plane is running and healthy: `forge healthcheck`

## Procedure

### Step 1: Update inventory

Add the new node to `deploy/ansible/inventory.yml`:

```yaml
    node3:
      ansible_host: <PUBLIC_IP>
      ansible_user: root
      forge_capacity_mb: 20000   # Adjust based on node RAM
```

Verify SSH access works:

```bash
ssh root@<PUBLIC_IP> 'hostname && uname -a'
```

### Step 2: Run the Ansible playbook

```bash
cd deploy/ansible
ansible-playbook -i inventory.yml playbook.yml --limit node3
```

This will:
1. Install base packages and tune inotify kernel parameters
2. Install Docker Engine 27.x (pinned, held from upgrades)
3. Install Tailscale and join the tailnet
4. Install forge-agent binary and start it as a systemd service

Expected duration: 3-5 minutes.

### Step 3: Verify node registration

The agent registers itself with the control plane on startup. Verify:

```bash
forge node list
```

Expected output:

```
ID         HOSTNAME    STATUS    CAPACITY    USED     SANDBOXES
<uuid>     node3       healthy   20000 MB    0 MB     0
```

If the node does not appear within 30 seconds, check agent logs:

```bash
ssh root@<PUBLIC_IP> 'journalctl -u forge-agent -n 50 --no-pager'
```

### Step 4: Verify Tailscale mesh connectivity

From the control plane node (or any other node in the fleet):

```bash
tailscale ping <NEW_NODE_TAILSCALE_IP>
```

Confirm direct peering (not DERP relay). If using DERP:

```
WARN: Connected via DERP relay. Fix: allow inbound UDP 41641 in Contabo firewall.
```

### Step 5: Verify Docker

```bash
ssh root@<PUBLIC_IP> 'docker version --format "{{.Server.Version}}"'
# Expected: 27.5.x
```

### Step 6: Create a test sandbox

```bash
forge sandbox create --app test-node3-$(date +%s) --image appx/sandbox:v1
```

Wait 10 seconds, then verify it was scheduled to the new node:

```bash
forge sandbox list --node <NEW_NODE_ID>
```

Clean up:

```bash
forge sandbox destroy <SANDBOX_ID>
```

## Troubleshooting

### Agent won't register

| Symptom | Cause | Fix |
|---------|-------|-----|
| `connection refused` in agent logs | Control plane not reachable | Check `FORGE_CONTROL_URL` in `/etc/forge/agent.env`. Verify Tailscale connectivity. |
| `401 Unauthorized` | API key issue | This is normal on first registration -- agent gets its token during registration. If it persists, check control plane logs. |
| `forge-agent` not running | Binary crash | `journalctl -u forge-agent -n 100`. Check if binary is the correct arch: `file /usr/local/bin/forge-agent` should show `ELF 64-bit LSB executable, x86-64`. |
| Agent starts but no heartbeats | Network timeout | Verify Tailscale is connected: `tailscale status`. Check firewall rules. |

### Docker issues

| Symptom | Fix |
|---------|-----|
| Wrong Docker version | Re-run playbook: it pins 27.x and holds the package |
| Docker Swarm active | `docker swarm leave --force` |
| Cannot pull sandbox image | Check registry access. Ensure Docker daemon is running: `systemctl status docker` |
| Container OOM kills | Check `forge_capacity_mb` is set correctly. Each sandbox uses ~256-512 MB. |

### Tailscale issues

| Symptom | Fix |
|---------|-----|
| DERP relay (not direct) | Allow inbound UDP 41641 in Contabo firewall |
| Auth key expired | Generate a new reusable key at Tailscale admin console |
| Node not in tailnet | `tailscale up --auth-key=<KEY> --accept-routes` |
| DNS resolution fails | Check Tailscale MagicDNS is enabled in admin console |

## Rollback

To remove a node from the fleet:

```bash
# 1. Drain the node (stops scheduling, existing sandboxes idle-reap)
forge node drain <NODE_ID>

# 2. Wait for sandboxes to finish or destroy them manually
forge sandbox list --node <NODE_ID>
forge sandbox destroy <SANDBOX_ID>  # for each remaining sandbox

# 3. Remove the node from the control plane
forge node remove <NODE_ID>

# 4. Remove from Tailscale
tailscale logout  # on the node
# Or remove from Tailscale admin console

# 5. (Optional) Stop and disable forge-agent
ssh root@<PUBLIC_IP> 'systemctl stop forge-agent && systemctl disable forge-agent'

# 6. Remove from inventory.yml
```
