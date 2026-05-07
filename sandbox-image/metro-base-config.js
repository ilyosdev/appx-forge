// Appx Metro base config — baked into the sandbox image at /opt/metro-base-config.js.
// Copied into /app/code/metro.config.js as 444 by entrypoint on every container start.
// User code cannot override this file (three-layer defense: entrypoint deletes any
// user-pushed metro.config.*; chmod 444; agent file-push handler rejects the path).
//
// Memory-tuning rationale: see .planning/phases/19-shared-bundler/19-B3-SPEC.md.
const { getDefaultConfig } = require('expo/metro-config');
const { FileStore } = require('metro-cache');
const path = require('node:path');
const crypto = require('node:crypto');

const projectRoot = '/app/code';
const config = getDefaultConfig(projectRoot);

// jest-worker pool collapsed into main process. Saves 300-500 MB on 4-core hosts
// (default spawns os.availableParallelism()/2 workers, each ~150-250 MB).
// Bundle wall-clock regresses 30-50% -- acceptable for dev preview.
config.maxWorkers = 1;

// ios/android/native for Expo Go; web for browser iframe preview.
config.resolver.platforms = ['ios', 'android', 'native', 'web'];

// Shrink file-map / haste-map footprint.
config.resolver.blockList = [
  /.*\/__(tests|mocks|fixtures|e2e)__\/.*/,
  /.*\/\.git\/.*/,
  /.*\/(android|ios)\/(build|Pods)\/.*/,
  /.*\/\.expo\/.*/,
  /.*\.d\.ts$/,
];

// Pin node_modules resolution to the shared read-only mount. No hierarchical walk.
config.resolver.disableHierarchicalLookup = true;
config.resolver.nodeModulesPaths = ['/opt/expo-shared-deps/node_modules'];

// Kill Watchman -- the single biggest hidden memory cost (300 MB - 1 GB daemon).
// Relies on Linux inotify; host sysctl tuning (99-metro.conf) raises watcher limits.
config.resolver.useWatchman = false;
config.watcher = { ...(config.watcher || {}), useWatchman: false };

// Disable lazy bundling. Prevents session-graph leak documented in facebook/metro#1191.
config.server = {
  ...(config.server || {}),
  rewriteRequestUrl: (url) =>
    url.replace('&lazy=true', '&lazy=false').replace('?lazy=true', '?lazy=false'),
};

// Shared persistent transform cache. Survives container restarts; transforms of
// framework files (react-native, expo-*) dedup across every tenant on the host.
config.cacheStores = [new FileStore({ root: '/mnt/metro-cache' })];

// Per-project cache namespace prevents cross-project cache poisoning
// (expo/expo#30930). Derived from the project's package.json name; falls back
// to a fixed tag if the file is unreadable for any reason.
let cacheVersion = 'appx-sandbox';
try {
  cacheVersion = require(path.join(projectRoot, 'package.json')).name || cacheVersion;
} catch {
  /* intentional: project may not have package.json yet on first boot */
}
config.cacheVersion = cacheVersion;

// Stable module IDs by SHA-1 of relative path. Makes cache keys stable across
// container restarts and across fleet-node instances (B4 foundation).
config.serializer.createModuleIdFactory = () => (modulePath) => {
  const rel = path.relative(projectRoot, modulePath);
  return parseInt(
    crypto.createHash('sha1').update(rel).digest('hex').slice(0, 8),
    16,
  );
};

module.exports = config;
