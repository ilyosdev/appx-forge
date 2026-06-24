#!/bin/bash
set -e

# Seed template files into bind-mounted /app/code.
# Two-phase seed:
#   1. -n (no-overwrite) copies USER-OWNED files if absent. Agent-written
#      code in subdirectories (app/, components/, ...) is preserved.
#   2. Framework files (entry.js, app.json, babel.config.js, package.json,
#      tsconfig.json) are refreshed when they DIFFER from the baked template.
#      These are infrastructure, not user code — when we ship a new sandbox
#      image (e.g. switching entry.js to expo-router/entry), existing
#      sandboxes' bind mounts must pick up the change without a wipe.
#
#      cmp-before-cp (v14): a sleep→wake (docker stop→start) re-runs this
#      entrypoint against the SAME preserved /app/code bind mount. An
#      unconditional `cp -f` rewrote these files every wake, bumping their
#      mtime, which made Metro's mtime-keyed TreeFS treat them as changed and
#      cold-rebundle on every wake. Copying only when the bytes differ leaves
#      mtimes untouched on an identical wake while still force-updating after a
#      genuine image bump. `cmp -s` exits non-zero when the dest is missing or
#      differs (→ copy), zero when identical (→ skip). First boot of a fresh
#      container: dest absent → cmp non-zero → copy, identical to before.
cp -rn /opt/template/. /app/code/ 2>/dev/null || true
for f in entry.js app.json babel.config.js package.json tsconfig.json; do
  if [ -f "/opt/template/$f" ]; then
    if ! cmp -s "/opt/template/$f" "/app/code/$f"; then
      cp -f "/opt/template/$f" "/app/code/$f"
    fi
  fi
done

# Symlink shared node_modules once (idempotent).
if [ ! -e /app/code/node_modules ]; then
  ln -sf /opt/expo-shared-deps/node_modules /app/code/node_modules
fi

# Enforce our tuned Metro config (three-layer defense vs. user/AI overrides).
# Layer 1: remove any user-pushed non-.js metro.config.* variants every start.
# These should never exist (forge-agent rejects metro.config.* pushes); rm -f of
# an absent file is a no-op and never touches metro.config.js's mtime.
rm -f /app/code/metro.config.ts \
      /app/code/metro.config.cjs \
      /app/code/metro.config.mjs

# Layer 2: install the baked config, but only rewrite when it DIFFERS (or is
# missing) — same cmp-before-cp wake fix as the framework files above. The old
# unconditional rm+cp rewrote metro.config.js every boot; a sleep→wake then
# bumped the config's mtime and Metro re-crawled the whole project. On an
# identical wake cmp matches → skip → mtime untouched → no rebundle. After a
# real image bump the baked config differs → rewrite. First boot: file absent
# → cmp non-zero → copy. We chmod 444 only when we actually (re)wrote it so a
# skipped wake performs zero filesystem mutation on this path.
if ! cmp -s /opt/metro-base-config.js /app/code/metro.config.js; then
  # File is 444 from a previous boot; make it writable before overwriting.
  chmod 644 /app/code/metro.config.js 2>/dev/null || true
  cp /opt/metro-base-config.js /app/code/metro.config.js
  chmod 444 /app/code/metro.config.js
fi

# Layer 3 lives in forge-agent: http/writer.go rejects file-push writes to
# metro.config.*. See appx-forge/agent/internal/http/writer.go.

# Teach Metro what public URL it's served under. Caddy fronts every sandbox
# on {appName}.{domain}:443 — without these env vars, Expo's CLI bakes
# ${metroHost}:8081 into the manifest's launchAsset.url and Expo Go then
# returns "Could not connect to development server" on scan because only
# 443 is exposed publicly.
#
# Precedence:
#   1. An explicitly-injected EXPO_PACKAGER_PROXY_URL wins (API can override).
#   2. Otherwise derive from APP_NAME (set by the API in forge.service.ts).
#   3. As a last resort, parse the container name `forge-${appName}` from
#      /etc/hostname — docker uses the container name here by default.
FORGE_DOMAIN="${FORGE_DOMAIN:-myappx.live}"
if [ -z "$APP_NAME" ] && [ -r /etc/hostname ]; then
  CN=$(cat /etc/hostname)
  if [ "${CN#forge-}" != "$CN" ]; then
    APP_NAME="${CN#forge-}"
  fi
fi
if [ -n "$APP_NAME" ]; then
  # EXPO_PACKAGER_PROXY_URL tells Expo CLI what URL to stamp into the
  # manifest's launchAsset.url. We deliberately DO NOT set
  # REACT_NATIVE_PACKAGER_HOSTNAME — it doubles as an override for where Metro
  # tries to bind/connect, and setting it to a public hostname sends Metro
  # into a "waiting on http://localhost:8081" stall because it thinks the
  # packager is remote.
  export EXPO_PACKAGER_PROXY_URL="${EXPO_PACKAGER_PROXY_URL:-https://${APP_NAME}.${FORGE_DOMAIN}}"
  echo "[entrypoint] EXPO_PACKAGER_PROXY_URL=${EXPO_PACKAGER_PROXY_URL}"
fi

# NOTE (v17): no node-boot cache pre-warm. A cold `expo export` cannot finish
# under the 0.5-CPU cap within a sane timeout (it would time out and thrash,
# re-attempting on every first-boot container). Instead the per-build CPU burst
# (ExecSpec.CPUBurst, set by the backend WebExportService) makes the FIRST real
# export on a fresh node fast AND warms the shared /mnt/metro-cache under the
# stable cacheVersion='appx-shared-v1' for every later project. See
# docs/bigpicture/server2-preview-speed-research-2026-06-24.md.

exec "$@"
