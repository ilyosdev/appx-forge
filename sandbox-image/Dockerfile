# Forge Sandbox Image v4 (B3 — memory-tuned Metro).
# See .planning/phases/19-shared-bundler/19-B3-SPEC.md for rationale.
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

EXPOSE 8081

HEALTHCHECK --interval=30s --timeout=10s --retries=3 --start-period=45s \
    CMD node -e "fetch('http://localhost:8081/status').then(r => r.ok ? process.exit(0) : process.exit(1)).catch(() => process.exit(1))"

WORKDIR /app/code
USER appuser
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/entrypoint.sh"]
CMD ["npx", "expo", "start", "--port", "8081"]
