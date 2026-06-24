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

// Transform worker pool. maxWorkers=1 (single worker, no jest-worker pool)
// minimised RAM but made the cold web-bundle compile pathologically slow — a
// ~2700-module web bundle took ~7.8 MINUTES single-threaded, which (before the
// backend prewarm + frontend spinner gate) showed as a black/slow preview.
// 2026-06-23: bumped 1 → 2. A second worker (~150-250 MB) lands a cold compile
// near ~650-730 MB, comfortably under the 1024 MB container cap (observed
// ~477 MB at maxWorkers=1), and roughly halves cold-compile wall-clock. The
// backend now also prewarms this bundle at provision/wake time so the user
// rarely waits on a cold compile at all. Do NOT raise to 3+ without re-checking
// peak RSS against the 1 GiB cap (Metro OOMs at the ceiling).
config.maxWorkers = 2;

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
// Boot watchdog + app-ready ping. The runtime-error hook in app/entry.js only
// fires on a THROWN error; an empty module graph or an Expo Router "Unmatched
// Route" tree renders nothing yet throws nothing → silent black screen while the
// backend reports "ready". This inline script runs in the raw browser context
// (so it works even if the Metro bundle never mounts) and POLLS #root:
//  - on real mounted content → posts {type:'appx:app-ready'} — the POSITIVE boot
//    signal the AppX frontend uses to clear the loading spinner and cancel its
//    hung-bundle auto-remount (PhonePreviewPanel.tsx). This is what kills the
//    "bundle 200 but iframe stuck black / black-during-compile" reload-black.
//  - on an Expo Router unmatched route → posts {type:'appx:runtime-error'}.
//  - if #root is STILL blank after MAX_MS → posts the same runtime-error.
// MAX_MS is generous on purpose: a cold Metro compile of the ~7.6MB web bundle
// takes ~30s, so the OLD single 4s check fired a premature false "rendered
// nothing" on every cold load (the bundle hadn't even executed yet). The poll +
// long ceiling fixes that. Fires exactly once (positive OR negative).
const BLANK_ROOT_WATCHDOG =
  '<script>\n' +
  '(function(){\n' +
  '  var POLL_MS = 400;\n' +
  '  var MAX_MS = 60000;\n' +
  '  var started = Date.now();\n' +
  '  var fired = false;\n' +
  '  function isUnmatchedRoute(root){\n' +
  '    try {\n' +
  "      var t = (root.textContent || '');\n" +
  "      return t.indexOf('Unmatched Route') !== -1 || t.indexOf('This screen does not exist') !== -1;\n" +
  '    } catch (e) { return false; }\n' +
  '  }\n' +
  '  function post(payload){\n' +
  '    try {\n' +
  '      if (window.parent && window.parent !== window) {\n' +
  "        window.parent.postMessage(payload, '*');\n" +
  '      }\n' +
  '    } catch (e) { /* cross-origin or disabled — never break preview */ }\n' +
  '  }\n' +
  '  function check(){\n' +
  '    if (fired) return;\n' +
  "    var root = document.getElementById('root');\n" +
  '    var hasContent = !!root && root.childElementCount > 0;\n' +
  '    var unmatched = !!root && isUnmatchedRoute(root);\n' +
  '    if (hasContent && !unmatched) {\n' +
  '      fired = true;\n' +
  "      post({ type: 'appx:app-ready', source: 'boot', timestamp: Date.now() });\n" +
  '      return;\n' +
  '    }\n' +
  '    if (unmatched) {\n' +
  '      fired = true;\n' +
  "      post({ type: 'appx:runtime-error', source: 'blank-root', message: 'App mounted but rendered an unmatched route', timestamp: Date.now() });\n" +
  '      return;\n' +
  '    }\n' +
  '    if (Date.now() - started >= MAX_MS) {\n' +
  '      fired = true;\n' +
  "      post({ type: 'appx:runtime-error', source: 'blank-root', message: 'App mounted but rendered nothing', timestamp: Date.now() });\n" +
  '      return;\n' +
  '    }\n' +
  '    setTimeout(check, POLL_MS);\n' +
  '  }\n' +
  '  setTimeout(check, POLL_MS);\n' +
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

// Cross-tenant shared transform cache: cacheVersion MUST be identical across
// EVERY project so framework transforms (react-native, expo-*, react) — which
// are byte-identical for all tenants (shared read-only /opt/expo-shared-deps +
// this same baked babel/metro config) — dedup in the shared /mnt/metro-cache
// FileStore (bind-mounted host volume, same across all sandboxes).
//
// It was previously derived from the project's package.json `name`, which is
// UNIQUE per project (e.g. "food-save-kz", "crypto-portfolio-tracker") → the
// cacheVersion differed per project → ZERO cross-project reuse → every cold
// `expo export` re-transformed all ~2200 modules (~8 min at the 0.5-CPU sandbox
// cap, > the build timeout). Metro keys each transform by file CONTENT + (this
// single shared) transformer options, so a stable cacheVersion cannot
// cross-contaminate files that differ; expo#30930's "poisoning" was a
// per-project babel/config divergence that does not exist here — every sandbox
// bakes this exact /opt/metro-base-config.js.
//
// BUMP this tag (…-v1 → -v2) whenever /opt/expo-shared-deps or the babel /
// transform config changes, so stale transforms are not reused after a rebuild.
config.cacheVersion = 'appx-shared-v1';

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
