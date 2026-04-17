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

exec "$@"
