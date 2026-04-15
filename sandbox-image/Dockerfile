# Forge Sandbox Image
# Runs Metro/Expo dev server for React Native app preview.
# Build: docker build -t forge-sandbox:v1 sandbox-image/
# Run:   docker run -p 8081:8081 -v /path/to/code:/app/code forge-sandbox:v1

FROM node:20-slim

# Install git (required by some Expo dependencies)
RUN apt-get update && \
    apt-get install -y --no-install-recommends git && \
    rm -rf /var/lib/apt/lists/*

# Create app user (UID 1000 -- agent must chown bind-mount dirs to match)
RUN useradd -u 1000 -m -s /bin/bash appuser

WORKDIR /app

# --- Layer 1: Install shared Expo/RN dependencies ---
# This is the heaviest layer (~400-500MB). Cached across builds.
COPY app/package.json /opt/expo-shared-deps/package.json
RUN cd /opt/expo-shared-deps && \
    npm install --legacy-peer-deps && \
    # Remove unnecessary files to reduce image size
    find /opt/expo-shared-deps/node_modules -name "*.md" -delete 2>/dev/null || true && \
    find /opt/expo-shared-deps/node_modules -name "CHANGELOG*" -delete 2>/dev/null || true && \
    find /opt/expo-shared-deps/node_modules -name "LICENSE*" -delete 2>/dev/null || true && \
    find /opt/expo-shared-deps/node_modules -name "*.d.ts.map" -delete 2>/dev/null || true && \
    echo "Shared deps size: $(du -sh /opt/expo-shared-deps/node_modules | cut -f1)"

# --- Layer 2: Copy app template ---
COPY app/ ./app/

# Symlink shared deps into app
RUN ln -sf /opt/expo-shared-deps/node_modules /app/app/node_modules

# Create code directory (will be bind-mounted by agent)
RUN mkdir -p /app/code && chown appuser:appuser /app/code

# Create cache directory for Metro
RUN mkdir -p /tmp/metro-cache && chown appuser:appuser /tmp/metro-cache

# Make expo CLI available in PATH
ENV NODE_PATH=/opt/expo-shared-deps/node_modules
ENV PATH="/opt/expo-shared-deps/node_modules/.bin:${PATH}"

# --- Runtime Configuration ---
ENV PORT=8081
ENV APP_NAME=sandbox
ENV NODE_ENV=development
# Metro needs development mode for HMR and fast refresh

EXPOSE 8081

# Health check: verify Metro is responding
HEALTHCHECK --interval=30s --timeout=10s --retries=3 --start-period=30s \
    CMD node -e "fetch('http://localhost:8081/status').then(r => r.ok ? process.exit(0) : process.exit(1)).catch(() => process.exit(1))"

# Run as non-root user
USER appuser

# Start Metro dev server
# Uses npx expo start which starts Metro on the configured port
CMD ["npx", "expo", "start", "--port", "8081", "--non-interactive"]
