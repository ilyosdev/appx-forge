#!/bin/bash
set -e

# Seed template files into bind-mounted /app/code.
# Two-phase seed:
#   1. -n (no-overwrite) copies USER-OWNED files if absent. Agent-written
#      code in subdirectories (app/, components/, ...) is preserved.
#   2. Framework files (entry.js, app.json, babel.config.js, package.json,
#      tsconfig.json) are FORCE-refreshed on every container start. These
#      are infrastructure, not user code — when we ship a new sandbox image
#      (e.g. switching entry.js to expo-router/entry), existing sandboxes'
#      bind mounts must pick up the change without requiring a wipe.
cp -rn /opt/template/. /app/code/ 2>/dev/null || true
for f in entry.js app.json babel.config.js package.json tsconfig.json; do
  if [ -f "/opt/template/$f" ]; then
    cp -f "/opt/template/$f" "/app/code/$f"
  fi
done

# Symlink shared node_modules once (idempotent).
if [ ! -e /app/code/node_modules ]; then
  ln -sf /opt/expo-shared-deps/node_modules /app/code/node_modules
fi

# Enforce our tuned Metro config (three-layer defense vs. user/AI overrides).
# Layer 1: remove any user-pushed metro.config.* at every container start.
rm -f /app/code/metro.config.js \
      /app/code/metro.config.ts \
      /app/code/metro.config.cjs \
      /app/code/metro.config.mjs

# Layer 2: copy baked config, mark read-only so appuser cannot modify at runtime.
cp /opt/metro-base-config.js /app/code/metro.config.js
chmod 444 /app/code/metro.config.js

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

exec "$@"
