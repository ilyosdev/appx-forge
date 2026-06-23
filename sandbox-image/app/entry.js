// Appx sandbox entry point. Installs runtime-error capture BEFORE delegating
// to Expo Router so errors during the initial module graph evaluation (e.g.
// a bad import or undefined property read in a top-level component) are
// caught and forwarded to the parent frame for the AI auto-fix loop.
//
// Forwards via window.postMessage to `window.parent` with a stable envelope:
//   { type: 'appx:runtime-error', source, message, stack?, file?, line?, timestamp }
// Receiver: frontend/src/components/canvas/PhonePreviewPanel.tsx
// Backend: handlePreviewRuntimeError in generation-gateway.handler.ts
//
// Design notes:
//  - Every registration is wrapped in try/catch so a missing platform API
//    can never prevent Metro from booting the app.
//  - Rate-limited per (source + message) key: 1 event per 2s.
//  - Drops LogBox/HMR noise and our own sentinel '[Preview]' prefix so we
//    don't create an infinite feedback loop.

(function installAppxErrorCapture() {
  var RATE_LIMIT_MS = 2000;
  var lastSentByKey = Object.create(null);

  function extractFileAndLine(stack) {
    if (!stack || typeof stack !== 'string') return {};
    // Match "Foo.tsx:123" or "Foo.tsx:123:45" anywhere in the stack
    var m = stack.match(/([A-Za-z0-9_.\-\/]+\.(?:tsx|ts|jsx|js)):(\d+)(?::(\d+))?/);
    if (!m) return {};
    return { file: m[1], line: parseInt(m[2], 10) };
  }

  function shouldDrop(message) {
    if (!message || typeof message !== 'string') return true;
    if (message.indexOf('[Preview]') !== -1) return true; // our own sentinel
    if (message.indexOf('[appx]') !== -1) return true;   // our own diagnostics
    if (message.indexOf('LogBox') !== -1) return true;
    if (message.indexOf('[HMR]') !== -1) return true;
    if (message.indexOf('hot update') !== -1) return true;
    // Warnings are not runtime errors we should auto-fix
    if (message.indexOf('Warning:') === 0) return true;
    return false;
  }

  function post(source, message, stack) {
    try {
      if (shouldDrop(message)) return;
      var key = source + '::' + String(message).slice(0, 200);
      var now = Date.now();
      if (lastSentByKey[key] && now - lastSentByKey[key] < RATE_LIMIT_MS) return;
      lastSentByKey[key] = now;

      var meta = extractFileAndLine(stack);
      var payload = {
        type: 'appx:runtime-error',
        source: source,
        message: String(message).slice(0, 2000),
        stack: stack ? String(stack).slice(0, 4000) : undefined,
        file: meta.file,
        line: meta.line,
        timestamp: now,
      };

      if (typeof window !== 'undefined' && window.parent && window.parent !== window) {
        try {
          window.parent.postMessage(payload, '*');
        } catch (_e) {
          // Cross-origin or disabled — fail silent, never break render.
        }
      }
    } catch (_e) {
      // Never throw from the error reporter itself.
    }
  }

  // 1) React Native's ErrorUtils (catches thrown errors during render)
  try {
    var g = typeof global !== 'undefined' ? global : (typeof window !== 'undefined' ? window : {});
    if (g && g.ErrorUtils && typeof g.ErrorUtils.setGlobalHandler === 'function') {
      var prev = typeof g.ErrorUtils.getGlobalHandler === 'function'
        ? g.ErrorUtils.getGlobalHandler()
        : null;
      g.ErrorUtils.setGlobalHandler(function (error, isFatal) {
        try {
          var msg = (error && error.message) ? error.message : String(error);
          var stk = (error && error.stack) ? error.stack : undefined;
          post('ErrorUtils', msg, stk);
        } catch (_e) {}
        // Preserve default behavior (LogBox, red screen, etc.)
        if (typeof prev === 'function') {
          try { prev(error, isFatal); } catch (_e) {}
        }
      });
    }
  } catch (_e) {}

  // 2) console.error wrap — catches React-reported render errors + library warnings
  try {
    if (typeof console !== 'undefined' && typeof console.error === 'function') {
      var origErr = console.error.bind(console);
      console.error = function () {
        try {
          var args = Array.prototype.slice.call(arguments);
          // Concatenate into a single message; preserve Error stacks when present
          var parts = [];
          var stk;
          for (var i = 0; i < args.length; i++) {
            var a = args[i];
            if (a && a.stack && !stk) stk = a.stack;
            if (a instanceof Error) parts.push(a.message);
            else if (typeof a === 'string') parts.push(a);
            else {
              try { parts.push(JSON.stringify(a)); } catch (_e) { parts.push(String(a)); }
            }
          }
          post('console', parts.join(' '), stk);
        } catch (_e) {}
        return origErr.apply(null, arguments);
      };
    }
  } catch (_e) {}

  // 3) window.onerror — browser runtime (expo-web runs in an iframe)
  try {
    if (typeof window !== 'undefined') {
      window.addEventListener('error', function (event) {
        try {
          var msg = (event && event.message) ? event.message : 'Unknown error';
          var stk = event && event.error && event.error.stack ? event.error.stack : undefined;
          post('window.onerror', msg, stk);
        } catch (_e) {}
      });
    }
  } catch (_e) {}

  // 4) Unhandled promise rejections
  try {
    if (typeof window !== 'undefined') {
      window.addEventListener('unhandledrejection', function (event) {
        try {
          var reason = event && event.reason;
          var msg;
          var stk;
          if (reason && typeof reason === 'object') {
            msg = reason.message ? reason.message : JSON.stringify(reason);
            stk = reason.stack;
          } else {
            msg = String(reason);
          }
          post('unhandledrejection', msg, stk);
        } catch (_e) {}
      });
    }
  } catch (_e) {}
})();

// ── Boot watchdog (reload-black cure) ────────────────────────────────────────
// Posts a POSITIVE paint signal the AppX preview frontend uses to clear its
// loading spinner — and, by its ABSENCE, to drive the hung-bundle auto-remount
// (PhonePreviewPanel.tsx). This lives in the bundle ENTRY rather than the served
// HTML shell on purpose: Expo SDK 54 (`web.output:"single"`) generates its own
// web index.html from app.json and serves it for `/`, shadowing any Metro
// `enhanceMiddleware` wrapper — so a shell-level <script> never reaches the
// browser (verified in prod: the served page is Expo's, titled "Appx Sandbox").
// entry.js IS the served bundle's first module, so this runs the instant the
// bundle executes.
//   - #root paints real content      -> {type:'appx:app-ready', source:'boot'}
//   - Expo Router 'Unmatched Route'   -> {type:'appx:runtime-error', source:'blank-root', message:'…unmatched route'}
//   - #root still empty at the ceiling-> {type:'appx:runtime-error', source:'blank-root', message:'…rendered nothing'}
// Fires exactly once. A bundle that never loads (502 / hung upstream) can't run
// this — the frontend's spinner fallback + known-ping fast-heal cover that
// reload case. MAX_MS is generous: a cold web bundle can take ~30s, so a short
// timeout would false-positive "rendered nothing" before the app even mounts.
(function installAppxBootWatchdog() {
  try {
    if (typeof window === 'undefined' || typeof document === 'undefined') return;
    if (!window.parent || window.parent === window) return;
    var POLL_MS = 400;
    var MAX_MS = 60000;
    var started = Date.now();
    var fired = false;
    function postParent(payload) {
      try { window.parent.postMessage(payload, '*'); } catch (_e) {}
    }
    function isUnmatched(root) {
      try {
        var t = root.textContent || '';
        return t.indexOf('Unmatched Route') !== -1 || t.indexOf('This screen does not exist') !== -1;
      } catch (_e) { return false; }
    }
    function check() {
      if (fired) return;
      var root = document.getElementById('root');
      var hasContent = !!root && root.childElementCount > 0;
      var unmatched = !!root && isUnmatched(root);
      if (hasContent && !unmatched) {
        fired = true;
        postParent({ type: 'appx:app-ready', source: 'boot', timestamp: Date.now() });
        return;
      }
      if (unmatched) {
        fired = true;
        postParent({ type: 'appx:runtime-error', source: 'blank-root', message: 'App mounted but rendered an unmatched route', timestamp: Date.now() });
        return;
      }
      if (Date.now() - started >= MAX_MS) {
        fired = true;
        postParent({ type: 'appx:runtime-error', source: 'blank-root', message: 'App mounted but rendered nothing', timestamp: Date.now() });
        return;
      }
      setTimeout(check, POLL_MS);
    }
    setTimeout(check, POLL_MS);
  } catch (_e) { /* never break boot */ }
})();

import 'expo-router/entry';
