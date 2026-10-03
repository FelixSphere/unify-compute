#!/usr/bin/env node
// Clicks the site's next/link links in headless Chrome, against a running
// instance of the site container:
//   node deploy/site/browser-check.mjs http://127.0.0.1:8080
// smoke.sh can prove the navigation payloads are served; only a browser can
// prove the router uses them. vinext 1.0.0-beta.5 threw "e is not a function"
// on every link click, which no curl check could see.
//
// No dependencies: Chrome DevTools Protocol over Node's built-in WebSocket.
// Chrome comes from $CHROME, else the usual install paths (GitHub's ubuntu
// runners ship google-chrome).

import { spawn } from 'node:child_process';
import { existsSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const base = process.argv[2]?.replace(/\/$/, '');
if (!base) {
  console.error('usage: browser-check.mjs BASE_URL');
  process.exit(2);
}
const origin = new URL(base).origin;

// The app host (ADR-0011) is the same container chosen by Host. Against a
// local container, Chrome is told the app name resolves to the container, so
// the page loads under its real origin and its real CSP. Against anything
// else the name already resolves where it should, or nowhere, so skip it.
const baseUrl = new URL(base);
const APP_HOST = 'app.compute.unifyai.us';
const appLocal = ['127.0.0.1', 'localhost'].includes(baseUrl.hostname);
const appOrigin = `${baseUrl.protocol}//${APP_HOST}${baseUrl.port ? `:${baseUrl.port}` : ''}`;

const chromePath =
  process.env.CHROME ||
  [
    '/usr/bin/google-chrome',
    '/usr/bin/google-chrome-stable',
    '/usr/bin/chromium',
    '/usr/bin/chromium-browser',
    '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  ].find(existsSync);
if (!chromePath) {
  console.error('FAIL no Chrome found; set CHROME');
  process.exit(1);
}

const profile = mkdtempSync(join(tmpdir(), 'site-browser-check-'));
const chrome = spawn(
  chromePath,
  [
    '--headless=new',
    '--remote-debugging-port=0',
    `--user-data-dir=${profile}`,
    '--no-first-run',
    '--no-default-browser-check',
    '--window-size=1280,900',
    // A system proxy would resolve the app name itself and never see the
    // mapping; a local container needs no proxy.
    ...(appLocal
      ? [
          `--host-resolver-rules=MAP ${APP_HOST} ${baseUrl.hostname}`,
          '--no-proxy-server',
        ]
      : []),
    // Ubuntu 24.04 runners restrict the user namespaces Chrome's sandbox uses.
    ...(process.env.CI ? ['--no-sandbox'] : []),
    'about:blank',
  ],
  { stdio: ['ignore', 'ignore', 'pipe'] },
);

let failed = false;
const fail = (msg) => {
  failed = true;
  console.log(`FAIL ${msg}`);
};
const ok = (msg) => console.log(`ok   ${msg}`);
const check = (cond, good, bad) => (cond ? ok(good) : fail(bad));

const exited = new Promise((resolve) => chrome.once('exit', resolve));
const cleanup = async () => {
  chrome.kill();
  await exited;
  // Chrome's helper processes can still be writing the profile after the
  // browser process exits. It is a temp dir; leaving it must not fail the run.
  try {
    rmSync(profile, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 });
  } catch {}
};
const watchdog = setTimeout(async () => {
  console.log('FAIL timed out after 90s');
  await cleanup();
  process.exit(1);
}, 90_000);

const wsUrl = await new Promise((resolve, reject) => {
  let buf = '';
  chrome.stderr.on('data', (d) => {
    buf += d;
    const m = buf.match(/DevTools listening on (ws:\/\/\S+)/);
    if (m) resolve(m[1]);
  });
  chrome.on('exit', (code) =>
    reject(new Error(`Chrome exited (${code}): ${buf}`)),
  );
});

const ws = new WebSocket(wsUrl);
await new Promise((resolve, reject) => {
  ws.onopen = resolve;
  ws.onerror = reject;
});

let nextId = 0;
const pending = new Map();
const listeners = new Set();
ws.onmessage = (e) => {
  const msg = JSON.parse(e.data);
  if (msg.id !== undefined && pending.has(msg.id)) {
    const { resolve, reject } = pending.get(msg.id);
    pending.delete(msg.id);
    if (msg.error) reject(new Error(msg.error.message));
    else resolve(msg.result);
  } else {
    for (const fn of listeners) fn(msg);
  }
};
const send = (method, params = {}, sessionId) =>
  new Promise((resolve, reject) => {
    const id = ++nextId;
    pending.set(id, { resolve, reject });
    ws.send(JSON.stringify({ id, method, params, sessionId }));
  });

const { targetId } = await send('Target.createTarget', { url: 'about:blank' });
const { sessionId } = await send('Target.attachToTarget', {
  targetId,
  flatten: true,
});
const page = (method, params) => send(method, params, sessionId);

// Third-party scripts (HubSpot) may log or throw on their own; only errors
// raised by this origin's code or requests count.
const errors = [];
let cspProbed = false;
const ours = (url) =>
  typeof url === 'string' &&
  (url.startsWith(origin) || (appLocal && url.startsWith(appOrigin)));
listeners.add((msg) => {
  if (msg.sessionId !== sessionId) return;
  const p = msg.params;
  if (msg.method === 'Runtime.exceptionThrown') {
    const d = p.exceptionDetails;
    const frames = d.stackTrace?.callFrames ?? [];
    if (
      ours(d.url) ||
      frames.some((f) => ours(f.url)) ||
      (!d.url && frames.length === 0)
    ) {
      errors.push(`uncaught: ${d.exception?.description ?? d.text}`);
    }
  } else if (msg.method === 'Runtime.consoleAPICalled' && p.type === 'error') {
    const frames = p.stackTrace?.callFrames ?? [];
    if (frames.length === 0 || frames.some((f) => ours(f.url))) {
      errors.push(
        `console.error: ${p.args.map((a) => a.value ?? a.description).join(' ')}`,
      );
    }
  } else if (
    msg.method === 'Log.entryAdded' &&
    p.entry.level === 'error' &&
    ours(p.entry.url)
  ) {
    errors.push(`${p.entry.text} (${p.entry.url})`);
  }
});

await page('Page.enable');
await page('Runtime.enable');
await page('Log.enable');

const evaluate = async (expression) => {
  const r = await page('Runtime.evaluate', {
    expression,
    returnByValue: true,
    awaitPromise: true,
  });
  if (r.exceptionDetails)
    throw new Error(`${expression}: ${r.exceptionDetails.text}`);
  return r.result.value;
};
const waitFor = async (expression, what, ms = 10_000) => {
  const until = Date.now() + ms;
  while (Date.now() < until) {
    if (await evaluate(expression).catch(() => false)) return true;
    await new Promise((r) => setTimeout(r, 100));
  }
  fail(`timed out waiting for ${what}`);
  return false;
};
const load = async (path, at = base) => {
  const loaded = new Promise((resolve) => {
    const fn = (msg) => {
      if (msg.sessionId === sessionId && msg.method === 'Page.loadEventFired') {
        listeners.delete(fn);
        resolve();
      }
    };
    listeners.add(fn);
  });
  await page('Page.navigate', { url: at + path });
  await loaded;
};

const HOME_TITLE = 'Unify Compute — On-demand inference & GPU compute';
const PRIVACY_TITLE = 'Privacy Policy — Unify Compute';
const COOKIE_LINK = '[aria-label="Cookie notice"] a[href="/privacy"]';
const FOOTER_LINK = 'footer a[href="/privacy"]';

// A marker on window survives a client-side navigation and is gone after a
// document load, which is how a broken router that falls back to full page
// loads is told apart from a working one.
const clickAndExpectSoftNav = async (selector, label) => {
  await evaluate(
    `window.__softNav = true; document.querySelector(${JSON.stringify(selector)}).click()`,
  );
  if (
    await waitFor(
      `location.pathname === '/privacy' && document.querySelector('h1')?.textContent === 'Privacy Policy'`,
      `${label} to reach /privacy`,
    )
  ) {
    check(
      await evaluate('window.__softNav === true'),
      `${label} navigated client-side`,
      `${label} reached /privacy by a full page load, not client-side navigation`,
    );
    check(
      (await evaluate('document.title')) === PRIVACY_TITLE,
      `${label}: title "${PRIVACY_TITLE}"`,
      `${label}: title is "${await evaluate('document.title')}"`,
    );
  }
};

const back = async () => {
  await evaluate('window.__softNav = true; history.back()');
  if (
    await waitFor(
      `location.pathname === '/' && !!document.querySelector(${JSON.stringify(FOOTER_LINK)})`,
      'back to /',
    )
  ) {
    check(
      await evaluate('window.__softNav === true'),
      'back to / client-side',
      'back to / was a full page load',
    );
    check(
      (await evaluate('document.title')) === HOME_TITLE,
      'title restored on /',
      'title not restored on /',
    );
  }
};

try {
  await load('/');
  // The cookie notice renders only after hydration, and a fresh profile has no
  // consent cookie, so its link appearing means the app is interactive.
  if (
    await waitFor(
      `!!document.querySelector(${JSON.stringify(COOKIE_LINK)})`,
      'hydration (cookie notice)',
    )
  ) {
    ok('/ hydrated, cookie notice shown');
  }
  check(
    (await evaluate('document.title')) === HOME_TITLE,
    `/ title "${HOME_TITLE}"`,
    '/ title wrong',
  );
  const hubspotLoaders = `document.querySelectorAll('#hs-script-loader').length`;
  check(
    (await evaluate(hubspotLoaders)) === 1,
    'HubSpot loader present once',
    'HubSpot loader not present exactly once',
  );

  await clickAndExpectSoftNav(FOOTER_LINK, 'footer "Privacy"');
  await back();
  await clickAndExpectSoftNav(COOKIE_LINK, 'cookie notice "Privacy Policy"');
  await back();
  check(
    (await evaluate(hubspotLoaders)) === 1,
    'HubSpot loader still present once after navigating',
    'HubSpot loader duplicated or lost by navigation',
  );

  await load('/privacy');
  check(
    (await evaluate(`document.querySelector('h1')?.textContent`)) ===
      'Privacy Policy',
    '/privacy full page load',
    '/privacy full page load did not render the policy',
  );

  await load('/does-not-exist');
  check(
    await evaluate(
      `document.body.innerText.includes('This page could not be found')`,
    ),
    '404 page',
    '404 page not rendered',
  );

  if (appLocal) {
    await load('/', appOrigin);
    check(
      (await evaluate('document.title')) === 'Unify Compute',
      `${APP_HOST} serves the app placeholder`,
      `${APP_HOST} title is "${await evaluate('document.title')}"`,
    );
    // The tokens and the stylesheet are external files; under a CSP with no
    // 'unsafe-inline' this proves both loaded and applied (--u-paper).
    const bg = await evaluate('getComputedStyle(document.body).backgroundColor');
    check(
      bg === 'rgb(250, 250, 247)',
      `${APP_HOST} styles applied under its CSP`,
      `${APP_HOST} body background is ${bg}; the stylesheets did not apply`,
    );
    check(
      await evaluate(
        `document.querySelector('.brand img')?.complete && document.querySelector('.brand img').naturalWidth > 0`,
      ),
      `${APP_HOST} lockup loaded`,
      `${APP_HOST} lockup image did not load`,
    );
    check(
      (await evaluate(`document.querySelectorAll('#hs-script-loader').length`)) === 0,
      `${APP_HOST} has no HubSpot loader`,
      `${APP_HOST} loads HubSpot`,
    );
    // The policy is enforced, not report-only: an injected inline script must
    // not run. Its violation message is expected, so it is not an error.
    cspProbed = true;
    const inlineRan = await evaluate(`new Promise((resolve) => {
      let violated = false;
      document.addEventListener('securitypolicyviolation', () => { violated = true; }, { once: true });
      const s = document.createElement('script');
      s.textContent = 'window.__inlineRan = true';
      document.head.appendChild(s);
      setTimeout(() => resolve({ ran: window.__inlineRan === true, violated }), 200);
    })`);
    check(
      !inlineRan.ran && inlineRan.violated,
      `${APP_HOST} CSP blocks inline script`,
      `${APP_HOST} CSP did not block an inline script (ran=${inlineRan.ran}, violation=${inlineRan.violated})`,
    );
  }
} catch (e) {
  // A broken router can leave the page somewhere the next step cannot run;
  // the errors collected so far are the useful part.
  fail(`stopped: ${e.message}`);
}

// Let late prefetches and their errors land before judging.
await new Promise((r) => setTimeout(r, 1000));
// The 404 document's own status is expected to show up as a resource error.
// Chrome ignores COOP on a plain-http origin that is not localhost, which the
// mapped app name is here; on AWS it is served over HTTPS.
const unexpected = errors.filter(
  (e) =>
    !e.includes('/does-not-exist') &&
    !(appLocal && e.includes('Cross-Origin-Opener-Policy header has been ignored')) &&
    !(
      cspProbed &&
      e.includes('Refused to execute inline script') &&
      !e.includes('[Report Only]')
    ),
);
if (unexpected.length)
  fail(`errors from ${origin}:\n  ${unexpected.join('\n  ')}`);
else ok('no console errors or failed requests from the site');

clearTimeout(watchdog);
ws.close();
await cleanup();
process.exit(failed ? 1 : 0);
