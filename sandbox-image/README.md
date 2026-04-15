# Forge Sandbox Image

Docker image for running Metro/Expo React Native dev server inside Forge sandbox containers.

## Build

```bash
docker build -t forge-sandbox:v1 sandbox-image/
```

## Run

```bash
# Basic run
docker run -p 8081:8081 forge-sandbox:v1

# With code bind-mount (production usage by agent)
docker run -p 8081:8081 \
  -v /var/lib/forge/sandboxes/my-app/code:/app/code \
  -e APP_NAME=my-app \
  forge-sandbox:v1
```

## Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| APP_NAME | No | sandbox | Application name (used for identification) |
| PORT | No | 8081 | Metro dev server port |
| NODE_ENV | No | development | Must be development for HMR to work |

## Ports

| Port | Protocol | Purpose |
|------|----------|---------|
| 8081 | HTTP | Metro dev server (bundles, status, HMR WebSocket) |

## Bind Mounts

| Container Path | Purpose |
|----------------|---------|
| /app/code | User code directory. Agent writes AI-generated files here. Metro watches for changes via inotify. |

## User

Container runs as `appuser` (UID 1000). The agent must `chown 1000:1000` the bind-mount directory before starting the container.

## Pre-installed Dependencies

The image includes ~30 common Expo/React Native packages pre-installed at `/opt/expo-shared-deps/node_modules/`. This includes:

- **Core**: expo, react, react-native, react-dom, react-native-web
- **Navigation**: @react-navigation/native, native-stack, bottom-tabs, expo-router
- **UI**: lucide-react-native, expo-blur, expo-linear-gradient, expo-image, expo-splash-screen, expo-status-bar
- **Gestures/Animation**: react-native-gesture-handler, react-native-reanimated, react-native-screens, react-native-safe-area-context
- **Device**: expo-haptics, expo-clipboard, expo-secure-store, expo-local-authentication, expo-linking
- **Media**: expo-image-picker, expo-file-system
- **State**: zustand, @react-native-async-storage/async-storage

AI-generated code can import any of these without additional npm install.

## Health Check

The container has a built-in health check that hits `http://localhost:8081/status` every 30s. Health checks start after a 30s startup grace period.

## Smoke Test

```bash
cd sandbox-image/
./smoke-test.sh [IMAGE_NAME]
```

Builds the image, runs a container, and verifies Metro responds. Reports cold start time.

## Image Size Target

Target: <500MB. The shared deps layer is the heaviest (~400MB). Unnecessary files (CHANGELOG, LICENSE, .d.ts.map) are pruned during build.
