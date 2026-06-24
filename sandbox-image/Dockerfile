# Forge Sandbox Image v17 (shared cacheVersion + per-build CPU burst).
# See .planning/phases/19-shared-bundler/19-B3-SPEC.md for B3 rationale.
#
# v17 changes (2026-06-24) — web-preview speed:
#   - metro-base-config.js: config.cacheVersion='appx-shared-v1' (stable
#     cross-tenant key) so the shared host /mnt/metro-cache FileStore dedups
#     framework transforms across ALL sandboxes (was per-project package.json
#     name → zero reuse → every cold export re-transformed ~2200 modules).
#   - Per-build CPU burst (agent ExecSpec.CPUBurst, set by the backend web
#     export) makes the FIRST real export on a fresh node run at a raised CPU
#     cap (~4 cores) so it finishes fast AND warms /mnt/metro-cache for every
#     later project's near-warm export. (No separate entrypoint pre-warm: a cold
#     export can't finish under the 0.5-CPU cap within a sane timeout.)
#
# v14 fix (2026-06-12): sleep-not-destroy wakes a sandbox via docker stop→start
# against the SAME preserved /app/code bind mount. The entrypoint then re-ran
# its framework-file + metro.config refresh with an UNCONDITIONAL `cp -f`, and
# the backend re-flushed the whole source_files tree on every wake. Both rewrote
# byte-identical files, bumping their mtimes; Metro's mtime-keyed TreeFS treated
# them as changed and cold-rebundled (adding seconds to every wake). v14 makes
# the entrypoint cmp-before-cp (copy only when bytes differ) — first boot of a
# fresh container is unchanged (dest absent → copy), an identical wake touches
# nothing. Companion changes: forge-agent's filepush writer skips identical-byte
# writes + only nudges Metro on a real change (writer.go), and the backend skips
# the wake reflush when the live bundle already equals the current revision
# (deployment.service.ts wakeContainer).
#
# v8 fix: template had no app/ subdir → Metro never added inotify watcher on
# app/ → AI-generated files pushed to app/ were invisible. Added app/index.tsx
# placeholder so Metro registers the watcher on startup. Also added 'web' to
# metro platforms for browser iframe route discovery.
#
# v5 fix: react-native-reanimated@4's validate-worklets-version.js requires
# `semver/functions/*` (v7 API). npm hoisted semver@6 to top-level (v6 has no
# functions/ subfolder). Metro config disables hierarchical node_modules lookup
# for haste-map footprint, so the nested semver@7 inside reanimated wasn't
# reachable. Pinning `semver ^7.7.2` as a direct dep in app/package.json forces
# npm to hoist v7 — nested v6 gets pushed down for any stray v6-only consumer.
FROM node:22-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends tini git ca-certificates && \
    rm -rf /var/lib/apt/lists/* && \
    userdel -r node 2>/dev/null || true && \
    useradd -u 1000 -m -s /bin/bash appuser

WORKDIR /app

# Layer 1: shared Expo/RN deps, installed once, mounted read-only by agent
# across all sandboxes. Removing docs trims the image.
COPY app/package.json /opt/expo-shared-deps/package.json
RUN cd /opt/expo-shared-deps && \
    npm install --legacy-peer-deps && \
    find /opt/expo-shared-deps/node_modules \
        \( -name '*.md' -o -name 'CHANGELOG*' -o -name 'LICENSE*' -o -name '*.d.ts.map' \) \
        -delete 2>/dev/null || true && \
    echo "Shared deps: $(du -sh /opt/expo-shared-deps/node_modules | cut -f1)"

# Layer 2: template (readonly source seed). Entrypoint copies into /app/code
# with -n so user-written files are preserved across restarts.
COPY app/ /opt/template/
# Remove macOS AppleDouble metadata files (._*) — these crash Metro's TypeScript
# parser when they land in app/ alongside real .tsx files.
RUN find /opt/template -name '._*' -delete 2>/dev/null || true

# Layer 3: baked Metro config. Entrypoint enforces it into /app/code/ as 444.
COPY metro-base-config.js /opt/metro-base-config.js

# User-writable code dir + shared transform cache mount target.
RUN mkdir -p /app/code /mnt/metro-cache && \
    chown appuser:appuser /app/code /mnt/metro-cache

COPY --chmod=755 entrypoint.sh /usr/local/bin/entrypoint.sh

# Memory flags on V8: 384 MB old-space ceiling, small semi-space, expose GC for
# future forced-collect on idle.
# --optimize-for-size is NOT ALLOWED in NODE_OPTIONS by Node.js policy; would need
# to be passed on the command line. Other flags still deliver the bulk of savings.
ENV NODE_OPTIONS="--max-old-space-size=384 --max-semi-space-size=2 --expose-gc"
ENV NODE_PATH=/opt/expo-shared-deps/node_modules
ENV PATH="/opt/expo-shared-deps/node_modules/.bin:${PATH}"
ENV PORT=8081
ENV NODE_ENV=development
ENV EXPO_NO_PROMPT=1
ENV EXPO_NO_METRO_LAZY=1
# Wake-crash RCA (2026-06-11) — defense-in-depth for the v12 fat-template
# crash: a /opt/template package.json claiming packages absent from
# /opt/expo-shared-deps made `expo start` exit with PACKAGE_NOT_FOUND on every
# container start (crash-watchdog restart loop, +60-120s per wake). The
# backend also injects this per-sandbox (forge.service.ts createApp env);
# baking it into the image covers sandboxes created outside that path. Users
# can't npm-install into sandboxes, so dependency validation is pure liability.
ENV EXPO_NO_DEPENDENCY_VALIDATION=1

EXPOSE 8081

HEALTHCHECK --interval=30s --timeout=10s --retries=3 --start-period=45s \
    CMD node -e "fetch('http://localhost:8081/status').then(r => r.ok ? process.exit(0) : process.exit(1)).catch(() => process.exit(1))"

WORKDIR /app/code
USER appuser
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/entrypoint.sh"]
CMD ["npx", "expo", "start", "--port", "8081"]
