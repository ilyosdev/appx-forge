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

// W3 — content negotiation at `/`. Browsers iframe-loading a sandbox URL expect
// an HTML wrapper that boots the JS bundle; Expo Go expects an Expo manifest JSON.
// Default Metro serves manifest at `/`, which caused iframes to render raw JSON
// text instead of the user app. Intercept GET `/` for browser-style Accept
// headers and serve a minimal Expo web bootstrap HTML wrapper; pass everything
// else through to Metro so Expo Go still gets manifest.
// Blank-root watchdog. The runtime-error hook in app/entry.js only fires on a
// THROWN error; an empty module graph or an Expo Router "Unmatched Route" tree
// renders nothing yet throws nothing → silent black screen while the backend
// reports "ready". This inline script (runs in the raw browser context, so it
// works even if the Metro bundle never mounts anything) checks #root ~4s after
// load and, if still blank, posts the SAME envelope app/entry.js uses
// ({type:'appx:runtime-error', source, message, timestamp}) so the existing
// AppX frontend handler (PhonePreviewPanel.tsx → handlePreviewRuntimeError)
// picks it up. Fires at most once.
const BLANK_ROOT_WATCHDOG =
  '<script>\n' +
  '(function(){\n' +
  '  var DELAY_MS = 4000;\n' +
  '  var fired = false;\n' +
  '  function isUnmatchedRoute(root){\n' +
  '    try {\n' +
  "      var t = (root.textContent || '');\n" +
  "      return t.indexOf('Unmatched Route') !== -1 || t.indexOf('This screen does not exist') !== -1;\n" +
  '    } catch (e) { return false; }\n' +
  '  }\n' +
  '  function check(){\n' +
  '    if (fired) return;\n' +
  "    var root = document.getElementById('root');\n" +
  '    var blank = !root || root.childElementCount === 0;\n' +
  '    var unmatched = root && isUnmatchedRoute(root);\n' +
  '    if (!blank && !unmatched) return;\n' +
  '    fired = true;\n' +
  '    try {\n' +
  '      if (window.parent && window.parent !== window) {\n' +
  '        window.parent.postMessage({\n' +
  "          type: 'appx:runtime-error',\n" +
  "          source: 'blank-root',\n" +
  "          message: unmatched ? 'App mounted but rendered an unmatched route' : 'App mounted but rendered nothing',\n" +
  '          timestamp: Date.now()\n' +
  "        }, '*');\n" +
  '      }\n' +
  '    } catch (e) { /* cross-origin or disabled — never break preview */ }\n' +
  '  }\n' +
  '  setTimeout(check, DELAY_MS);\n' +
  '})();\n' +
  '</script>\n';

const HTML_WRAPPER =
  '<!DOCTYPE html>\n' +
  '<html lang="en">\n' +
  '<head>\n' +
  '<meta charset="UTF-8">\n' +
  '<meta name="viewport" content="width=device-width, initial-scale=1.0">\n' +
  '<title>AppX Preview</title>\n' +
  '<style>html,body,#root{margin:0;padding:0;height:100%}#root{display:flex;flex:1}</style>\n' +
  '</head>\n' +
  '<body>\n' +
  '<noscript>You need to enable JavaScript to run this app.</noscript>\n' +
  '<div id="root"></div>\n' +
  '<script src="/entry.bundle?platform=web&dev=true&hot=false&lazy=false&transform.engine=hermes&transform.routerRoot=app&unstable_transformProfile=hermes-stable" defer></script>\n' +
  BLANK_ROOT_WATCHDOG +
  '</body>\n' +
  '</html>\n';
function acceptQuality(accept, mime) {
  const entries = accept.split(',').map((s) => s.trim().toLowerCase());
  for (const e of entries) {
    const [type, ...params] = e.split(';').map((s) => s.trim());
    const matches = type === mime || type === '*/*' || (type.endsWith('/*') && mime.startsWith(type.slice(0, -2)));
    if (!matches) continue;
    const qParam = params.find((p) => p.startsWith('q='));
    return qParam ? parseFloat(qParam.slice(2)) : 1;
  }
  return 0;
}
function preferHtmlOverManifest(req) {
  const expoHeader = (req.headers['expo-platform'] || req.headers['exponent-platform'] || '').toString();
  if (expoHeader) return false;
  const accept = (req.headers['accept'] || '').toLowerCase();
  if (!accept) return true;
  if (accept.includes('application/expo+json') || accept.includes('multipart/mixed')) return false;
  return acceptQuality(accept, 'text/html') >= acceptQuality(accept, 'application/json');
}
// Disable lazy bundling. Prevents session-graph leak documented in facebook/metro#1191.
config.server = {
  ...(config.server || {}),
  rewriteRequestUrl: (url) =>
    url.replace('&lazy=true', '&lazy=false').replace('?lazy=true', '?lazy=false'),
  enhanceMiddleware: (metroMiddleware) => {
    return (req, res, next) => {
      if (
        (req.method === 'GET' || req.method === 'HEAD') &&
        (req.url === '/' || req.url === '/index.html' || req.url.startsWith('/?'))
      ) {
        if (preferHtmlOverManifest(req)) {
          res.setHeader('Content-Type', 'text/html; charset=utf-8');
          res.setHeader('Cache-Control', 'no-store');
          res.end(req.method === 'HEAD' ? '' : HTML_WRAPPER);
          return;
        }
      }
      return metroMiddleware(req, res, next);
    };
  },
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
