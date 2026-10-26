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
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
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
  console.log('FAIL timed out after 180s');
  await cleanup();
  process.exit(1);
}, 180_000);

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
// FelixSphere's Terms and Conditions (app/links.ts TERMS_URL; ADR-0011 D2).
const TERMS_URL = 'https://felixsphere.com/legal/terms';
const termsLinks = (selector) =>
  evaluate(`[...document.querySelectorAll(${JSON.stringify(selector)})].filter((a) => /terms/i.test(a.textContent)).map((a) => a.textContent.trim() + '|' + a.getAttribute('href') + '|' + (a.target || ''))`);
const deadTerms = `document.querySelectorAll('a[href*="compute.unifyai.us/terms"]').length`;
// UI-STANDARD 2026-10-03: page footers carry the brand only; the legal page
// adds the legal entity. The Malaysian company line is gone.
const BRAND_LINE = `© ${new Date().getFullYear()} UnifyAI`;
const ENTITY_LINE = 'Operated by FelixSphere LLC · 6 Karen Ct, CA 94010, United States';
const COMPANY_LINE = `document.querySelector('footer .copyright')?.innerText.trim() ?? null`;

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

const START_TITLE = 'Get started — Unify Compute';
const START_H1 = 'Get started with Unify Compute';
// Production's app host, on staging too: the image is shared. See app/links.ts.
const LOGIN = 'https://app.compute.unifyai.us/login';
const REGISTER = 'https://app.compute.unifyai.us/signup';

const setViewport = (width, height, mobile) =>
  page('Emulation.setDeviceMetricsOverride', {
    width,
    height,
    deviceScaleFactor: 1,
    mobile,
  });

// Rendered and inside the viewport horizontally: display:none, hidden, or a
// link pushed off the right edge all fail.
const visible = (selector) => `(() => {
  const el = document.querySelector(${JSON.stringify(selector)});
  if (!el) return false;
  const r = el.getBoundingClientRect();
  return r.width > 0 && r.height > 0 && r.left >= 0 && r.right <= innerWidth;
})()`;

const clickToStart = async (selector, label) => {
  await evaluate(
    `window.__softNav = true; document.querySelector(${JSON.stringify(selector)}).click()`,
  );
  if (
    await waitFor(
      `location.pathname === '/start' && document.querySelector('h1')?.textContent === ${JSON.stringify(START_H1)}`,
      `${label} to reach /start`,
    )
  ) {
    check(
      await evaluate('window.__softNav === true'),
      `${label} navigated client-side`,
      `${label} reached /start by a full page load, not client-side navigation`,
    );
    check(
      (await evaluate('document.title')) === START_TITLE,
      `${label}: title "${START_TITLE}"`,
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

// The app host's pages (dashboard/, plan task A7b). compute-api is not in
// this container, so /dashboard/v1/* is answered here, in the browser, by
// stubs in the shapes of docs/spec/accounts.md; every call is recorded so the
// request bodies and headers can be checked. Every other request is recorded
// too, to prove nothing leaves the origin and the reset token stays out of
// URLs and Referer.
const apiCalls = [];
const requests = [];
let stubs = {};
const checkoutVisits = [];
let checkoutReturn = '';
listeners.add((msg) => {
  if (msg.sessionId !== sessionId) return;
  if (msg.method === 'Network.requestWillBeSent') {
    requests.push({ url: msg.params.request.url, headers: msg.params.request.headers, document: msg.params.documentURL });
  } else if (msg.method === 'Fetch.requestPaused') {
    const { requestId, request } = msg.params;
    // The top-up's redirect to Stripe Checkout never reaches Stripe: the
    // "payment page" answers at once by sending the browser back the way
    // Stripe's success_url (or cancel_url) would.
    if (new URL(request.url).hostname === 'checkout.stripe.com') {
      checkoutVisits.push(request.url);
      send(
        'Fetch.fulfillRequest',
        { requestId, responseCode: 302, responseHeaders: [{ name: 'Location', value: checkoutReturn }], body: '' },
        sessionId,
      ).catch((e) => fail(`stub for Stripe Checkout: ${e.message}`));
      return;
    }
    const path = new URL(request.url).pathname.replace(/^\/dashboard\/v1/, '');
    let body;
    try {
      body = request.postData ? JSON.parse(request.postData) : undefined;
    } catch {
      body = request.postData;
    }
    const call = { method: request.method, path, url: request.url, headers: request.headers, body };
    apiCalls.push(call);
    // A stub may be a function of the call, for answers that change (a
    // reauth, then the retried request). `text` answers text/plain.
    let stub = stubs[`${request.method} ${path}`];
    if (typeof stub === 'function') stub = stub(call);
    stub ??= {
      status: 404,
      body: { error: { type: 'not_found', code: 'not_found', message: 'Not found.' } },
    };
    send(
      'Fetch.fulfillRequest',
      {
        requestId,
        responseCode: stub.status,
        responseHeaders: [
          { name: 'Content-Type', value: stub.text === undefined ? 'application/json' : 'text/plain; charset=utf-8' },
          ...(stub.headers ?? []),
        ],
        body: Buffer.from(stub.text ?? JSON.stringify(stub.body ?? {})).toString('base64'),
      },
      sessionId,
    ).catch((e) => fail(`stub for ${path}: ${e.message}`));
  }
});

const shots = process.env.SCREENSHOT_DIR;
const shot = async (name) => {
  if (!shots) return;
  mkdirSync(shots, { recursive: true });
  const { data } = await page('Page.captureScreenshot', { format: 'png', captureBeyondViewport: true });
  writeFileSync(join(shots, `${name}.png`), Buffer.from(data, 'base64'));
};
const js = (v) => JSON.stringify(v);
const setValue = (sel, value) =>
  evaluate(`(() => { const el = document.querySelector(${js(sel)}); el.value = ${js(value)}; el.dispatchEvent(new Event('input', { bubbles: true })); })()`);
const blur = (sel) => evaluate(`(() => { const el = document.querySelector(${js(sel)}); el.focus(); el.blur(); })()`);
const click = (sel) => evaluate(`document.querySelector(${js(sel)}).click()`);
const text = (sel) => evaluate(`document.querySelector(${js(sel)})?.textContent ?? null`);
const isShown = (sel) =>
  evaluate(`(() => { const el = document.querySelector(${js(sel)}); return !!el && !el.closest('[hidden]') && el.getClientRects().length > 0; })()`);
const DANGER = 'rgb(217, 45, 32)';
// The session object login, verify and /me answer (A4's dashboard-openapi.json).
const SESSION = {
  user: { email: 'person@example.com', created_at: 1791000000 },
  account: { id: 1, status: 'active', compute_enabled: false },
  csrf_token: 'csrf-1',
};
const err = (code, message, param, status = 400, type = 'invalid_request') => ({
  status,
  body: { error: { type, code, message, ...(param ? { param } : {}) } },
});
const appLoad = async (path) => {
  await load(path, appOrigin);
  await waitFor(`document.readyState === 'complete'`, `${path} to load`);
};
const fieldErrorShown = async (field, want, label) => {
  const ok =
    (await waitFor(`!!document.querySelector('#${field}-error:not([hidden])')`, `${label}: error under ${field}`)) &&
    (await text(`#${field}-error`)) === want &&
    (await evaluate(`document.querySelector('#${field}').getAttribute('aria-invalid')`)) === 'true' &&
    (await evaluate(`getComputedStyle(document.querySelector('#${field}-error')).color`)) === DANGER;
  check(ok, `${label}: "${want}" under ${field} in --u-danger`, `${label}: field error under ${field} is "${await text(`#${field}-error`)}"`);
};
const formErrorShown = async (want, label) => {
  const ok =
    (await waitFor(`!!document.querySelector('#form-error:not([hidden])')`, `${label}: form-level error`)) &&
    (await text('#form-error p'))?.startsWith(want) &&
    (await evaluate(`document.querySelector('#form-error').getAttribute('role')`)) === 'alert';
  check(ok, `${label}: form-level error "${want}" (role=alert)`, `${label}: form-level error is "${await text('#form-error p')}"`);
};
const tabOrder = async (n) => {
  await evaluate('document.activeElement?.blur(); window.getSelection()?.removeAllRanges()');
  const seen = [];
  for (let i = 0; i < n; i++) {
    for (const type of ['keyDown', 'keyUp'])
      await page('Input.dispatchKeyEvent', { type, key: 'Tab', code: 'Tab', windowsVirtualKeyCode: 9 });
    seen.push(
      await evaluate(`(() => { const el = document.activeElement; if (!el) return ''; if (el.id) return '#' + el.id; if (el.classList.contains('toggle')) return 'toggle:' + el.getAttribute('aria-controls'); return el.tagName.toLowerCase() + ':' + (el.textContent || '').trim(); })()`),
    );
  }
  return seen;
};
const noHorizontalScroll = async (path) => {
  await page('Emulation.setDeviceMetricsOverride', { width: 375, height: 812, deviceScaleFactor: 2, mobile: true });
  await appLoad(path);
  const [sw, iw] = await evaluate('[document.documentElement.scrollWidth, window.innerWidth]');
  check(sw <= iw, `${path} at 375px: no horizontal scroll`, `${path} at 375px scrolls sideways (${sw} > ${iw})`);
  await shot(`${path === '/' ? 'home' : path.slice(1).replaceAll('/', '-')}-375`);
  await page('Emulation.clearDeviceMetricsOverride');
};

// The signed-in pages (dashboard/, plan task A7c), against stubs in the shapes
// compute-api answers for docs/spec/accounts.md's session routes (all built
// since A7d; compute-api/internal/dashboard and dashboard-openapi.json). Each
// page is also checked with the route missing (404 not_found), which is what
// a compute-api older than A7d answers.
const CSRF = 'csrf-dash-7';
const ME = {
  user: { email: 'jane.doe@example.com', created_at: 1791000000 },
  account: { id: 42, status: 'active', compute_enabled: false },
  csrf_token: CSRF,
};
const JOB_ID = 'job_01J8Q6ZK3R7Y9W2X4V6T8S0A1B';
const RUNNING = {
  id: JOB_ID,
  object: 'compute.job',
  status: 'running',
  sku: 'a10g-24gb-x1',
  created_at: 1791000100,
  started_at: 1791000142,
  finished_at: null,
  spec: { image: 'ghcr.io/acme/embed:1.4', command: ['python', 'run.py'], max_runtime_seconds: 7200 },
  usage: { gpu_seconds: 1830, estimated_cost_usd: '2.5400', settled_cost_usd: '0.8600', hold_usd: '2.5400' },
  error: null,
  placement: { cloud: 'aws', region: 'us-east-1', instance_type: 'g5.xlarge' },
  metadata: { pipeline: 'nightly-embed' },
};
const DONE = { ...RUNNING, id: 'job_01J8Q6ZK3R7Y9W2X4V6T8S0A0Z', status: 'succeeded', finished_at: 1791003000, usage: { ...RUNNING.usage, hold_usd: '0.0000' } };
const CANCELLED = { ...RUNNING, status: 'cancelled', finished_at: 1791002000, usage: { ...RUNNING.usage, hold_usd: '0.0000' } };
const KEY = { id: 7, name: 'nightly pipeline', display: 'uck_AbCd1234', scopes: ['jobs', 'read'], created_at: 1791000000, expires_at: null, last_used_at: 1791000500, revoked_at: null };
const SECRET = 'uck_0123456789abcdefghijABCDEFGHIJ0123456789';
const NEW_KEY = { id: 8, name: 'ci', display: 'uck_01234567', scopes: ['jobs', 'read'], created_at: 1791001000, expires_at: null, last_used_at: null, revoked_at: null };
const ARTIFACT_URL = 'https://unify-compute-production-artifacts.s3.us-east-1.amazonaws.com/jobs/x/results.jsonl?X-Amz-Signature=abc';
const okBody = (body, status = 200) => ({ status, body });
const notFound = { status: 404, body: { error: { type: 'not_found', code: 'not_found', message: 'Not found.' } } };
const sessionGone = err('session_required', 'Log in to continue.', null, 401, 'authentication_error');

const entry = (e, job) => ({
  object: 'compute.balance_entry',
  job_id: job ?? null,
  ref: job ? { type: 'job', id: job } : null,
  detail: job ? { sku: 'a10g-24gb-x1' } : {},
  ...e,
});

const TOPUP_ID = 'tu_01J8Q6ZK3R7Y9W2X4V6T8S0A2C';
const TOPUP_OLD = { id: 'tu_01J8Q6ZK3R7Y9W2X4V6T8S0A0T', object: 'compute.topup', amount_usd: '15.00', status: 'credited', created_at: 1791000000, paid_at: 1791000030, receipt_url: 'https://pay.stripe.com/receipts/payment/abc', refunded_usd: '0.00' };
const TOPUP_NEW = { id: TOPUP_ID, object: 'compute.topup', amount_usd: '20.00', status: 'open', created_at: 1791001000, paid_at: null, receipt_url: null, refunded_usd: '0.00' };
const topupList = (data, enabled = true) => okBody({ object: 'list', data, has_more: false, topups_enabled: enabled });

let keysList = [KEY];
const baseStubs = () => ({
  'GET /topups': topupList([TOPUP_OLD]),
  'GET /me': okBody(ME),
  'POST /logout': { status: 204 },
  'GET /balance': okBody({ object: 'compute.balance', currency: 'USD', available_usd: '12.5000', held_usd: '2.5400', limits: { topup_min_usd: '10.00', topup_max_usd: '100.00', balance_max_usd: '200.00', topups_per_day: 3 } }),
  'GET /jobs': okBody({ object: 'list', data: [RUNNING, DONE], has_more: false }),
  [`GET /jobs/${JOB_ID}`]: okBody(RUNNING),
  [`GET /jobs/${JOB_ID}/events`]: okBody({
    object: 'list',
    data: [
      { id: 'evt_1', object: 'compute.event', job_id: JOB_ID, type: 'job.queued', created_at: 1791000100, data: { sku: 'a10g-24gb-x1' } },
      { id: 'evt_2', object: 'compute.event', job_id: JOB_ID, type: 'job.placed', created_at: 1791000140, data: { placement: RUNNING.placement } },
    ],
    has_more: false,
  }),
  [`GET /jobs/${JOB_ID}/logs`]: { status: 200, text: 'loading model\nstep 2 of 9' },
  [`GET /jobs/${JOB_ID}/artifacts`]: okBody({
    object: 'list',
    data: [{ object: 'compute.artifact', path: '/mnt/out/results.jsonl', size: 18234, sha256: '9f86d081884c7d659a2feaa0c55ad015', url: ARTIFACT_URL, expires_at: 1791003600 }],
    complete: true,
    truncated: false,
  }),
  'GET /keys': () => okBody(keysList),
  // An operator credit has no ref (its audit id and reason are internal); a
  // hold's entries name the job, not the hold.
  'GET /transactions': okBody({
    object: 'list',
    data: [
      entry({ id: 'be_01J8Q6ZK3R7Y9W2X4V6T8S0A03', kind: 'settle', amount_usd: '-0.8600', available_delta_usd: '0.0000', held_delta_usd: '-0.8600', available_after_usd: '12.5000', held_after_usd: '1.6800', created_at: 1791000400 }, JOB_ID),
      entry({ id: 'be_01J8Q6ZK3R7Y9W2X4V6T8S0A02', kind: 'hold', amount_usd: '-2.5400', available_delta_usd: '-2.5400', held_delta_usd: '2.5400', available_after_usd: '12.5000', held_after_usd: '2.5400', created_at: 1791000100 }, JOB_ID),
      entry({ id: 'be_01J8Q6ZK3R7Y9W2X4V6T8S0A01', kind: 'admin_credit', amount_usd: '15.0000', available_delta_usd: '15.0000', held_delta_usd: '0.0000', available_after_usd: '15.0000', held_after_usd: '0.0000', created_at: 1791000000 }),
    ],
    has_more: false,
  }),
  'GET /usage': okBody({
    object: 'compute.usage',
    month: '2026-10',
    currency: 'USD',
    gpu_seconds: 10200,
    gpu_usd: '3.4000',
    storage_usd: '0.0000',
    egress_usd: '0.0000',
    total_usd: '3.4000',
    by_sku: [{ sku: 'a10g-24gb-x1', gpu_seconds: 10200, usd: '3.4000' }],
  }),
});

const visibleCount = (sel) =>
  evaluate(`[...document.querySelectorAll(${js(sel)})].filter((el) => !el.closest('[hidden]') && el.getClientRects().length > 0).length`);
const onePrimary = async (label) => {
  const n = await visibleCount('.primary');
  check(n <= 1, `${label}: at most one primary button (${n})`, `${label}: ${n} primary buttons in view`);
};
const pressKey = async (key, code, keyCode) => {
  for (const type of ['keyDown', 'keyUp'])
    await page('Input.dispatchKeyEvent', { type, key, code, windowsVirtualKeyCode: keyCode, ...(type === 'keyDown' && key === 'Enter' ? { text: '\r' } : {}) });
};
// Every state-changing call carries the CSRF token, and the token came from
// a GET /me made earlier on the page (since #131 the page reuses it and
// re-fetches only on 403 csrf_failed, instead of a GET /me per action).
const csrfChecked = (label, from) => {
  const calls = apiCalls.slice(from);
  const mutating = calls.filter((c) => c.method !== 'GET');
  const bad = mutating.filter((c) => {
    const i = apiCalls.indexOf(c);
    const prior = apiCalls.slice(0, i).reverse().find((p) => p.method === 'GET' && p.path === '/me');
    return c.headers['X-CSRF-Token'] !== CSRF || !prior;
  });
  check(
    mutating.length > 0 && bad.length === 0,
    `${label}: ${mutating.map((c) => `${c.method} ${c.path}`).join(', ')} sent X-CSRF-Token from the page's GET /me`,
    `${label}: CSRF missing or not re-fetched on ${bad.map((c) => `${c.method} ${c.path}`).join(', ') || '(no mutating call seen)'}`,
  );
};

async function dashboardChecks() {
  await page('Emulation.clearDeviceMetricsOverride');
  stubs = baseStubs();

  // --- Overview: the shell, the focal balance, compute access, recent jobs.
  await appLoad('/');
  await waitFor(`document.querySelector('#balance-available')?.textContent === '12.50'`, 'overview balance');
  const shellBox = await evaluate(`(() => {
    const r = (s) => document.querySelector(s).getBoundingClientRect();
    const cur = document.querySelector('.app-nav a[aria-current="page"]');
    return { header: r('.app-header').height, nav: r('.app-nav').width, cur: cur?.getAttribute('href'), curBg: getComputedStyle(cur).backgroundColor,
      avatar: r('.identity .avatar').width, initials: document.querySelector('#account-initials').textContent,
      metric: getComputedStyle(document.querySelector('.stat-card.hero .stat-value')).fontSize, metricFont: getComputedStyle(document.querySelector('.stat-card.hero .stat-value')).fontFamily };
  })()`);
  check(
    shellBox.header === 64 && shellBox.nav === 216 && shellBox.cur === '/' && shellBox.curBg === 'rgb(14, 14, 14)' && shellBox.avatar === 36 && shellBox.initials === 'JD',
    'app shell: 64px header, 216px sidebar, current item in ink, 36px account button with initials',
    `app shell: ${JSON.stringify(shellBox)}`,
  );
  check(
    shellBox.metric === '40px' && shellBox.metricFont.includes('Space Grotesk'),
    'focal number in Space Grotesk 40px (W8: the first stat card, not a 64px hero)',
    `focal number is ${shellBox.metric} ${shellBox.metricFont}`,
  );
  // Once the data is in, no placeholder may still be a shimmering skeleton
  // (#136 shipped text lines that kept the skeleton after being filled).
  await waitFor(`!!document.querySelector('#recent-jobs a[href^="/jobs/"]')`, 'overview data loaded');
  const stillSkeleton = await evaluate(`[...document.querySelectorAll('main *')].filter((e) => getComputedStyle(e).animationName === 'uc-shimmer').map((e) => e.id || e.className || e.tagName)`);
  check(stillSkeleton.length === 0, 'overview: no skeleton left once loaded', `overview: still a skeleton after load: ${stillSkeleton.join(', ')}`);
  check(
    (await text('#balance-sub')).includes('2.54'),
    'overview shows the held amount under the balance',
    `overview balance sub-line: ${await text('#balance-sub')}`,
  );
  check(
    (await text('#access-word')) === 'Awaiting enablement' &&
      (await evaluate(`document.querySelector('#access-text a')?.getAttribute('href')`))?.startsWith('mailto:contactus@felixsphere.com') &&
      (await evaluate(`getComputedStyle(document.querySelector('#access-dot')).width`)) === '8px',
    'compute access: dot + "Awaiting enablement" + mailto contact',
    `compute access: "${await text('#access-word')}"`,
  );
  await waitFor(`!!document.querySelector('#recent-jobs a[href="/jobs/${JOB_ID}"]')`, 'recent jobs to link to the job page');
  check(
    (await evaluate(`getComputedStyle(document.querySelector('#recent-jobs th')).fontFamily`)).includes('IBM Plex Mono') &&
      (await evaluate(`getComputedStyle(document.querySelector('#recent-jobs td .mono')).fontFamily`)).includes('IBM Plex Mono'),
    'tables: mono headers, mono IDs',
    'tables: headers or IDs are not mono',
  );
  await onePrimary('/');
  // The policy is enforced on the signed-in pages too.
  const inline = await evaluate(`new Promise((resolve) => {
    let violated = false;
    document.addEventListener('securitypolicyviolation', () => { violated = true; }, { once: true });
    const s = document.createElement('script');
    s.textContent = 'window.__dashInline = true';
    document.head.appendChild(s);
    setTimeout(() => resolve({ ran: window.__dashInline === true, violated }), 200);
  })`);
  check(!inline.ran && inline.violated, 'overview: CSP blocks inline script', `overview: inline script ran=${inline.ran} violation=${inline.violated}`);
  await shot('overview-desktop');

  // W9: the header shows who is signed in (a link to Account), Log out in
  // the open, and the language picker with UI-STANDARD's ten languages.
  const header = await evaluate(`(() => ({
    account: document.querySelector('#account-button')?.getAttribute('href'),
    email: document.querySelector('#account-email')?.textContent,
    logout: document.querySelector('#logout')?.getClientRects().length,
    langs: [...document.querySelectorAll('#locale-picker option')].map((o) => o.value) }))()`);
  check(
    header.account === '/account' && header.email === ME.user.email && header.logout > 0 && header.langs.length === 10 && header.langs[0] === 'en' && header.langs[9] === 'ar',
    'header: identity links to /account, Log out visible, ten languages in the picker',
    `header: ${JSON.stringify(header)}`,
  );

  stubs['GET /me'] = okBody({ ...ME, account: { ...ME.account, compute_enabled: true } });
  await appLoad('/');
  await waitFor(`document.querySelector('#access-word')?.textContent === 'Enabled'`, 'compute access "Enabled"');
  ok('compute access shows "Enabled" when compute_enabled');
  stubs = baseStubs();

  // --- API keys: create (reauth first), the key shown once, revoke.
  await appLoad('/keys');
  await waitFor(`document.querySelector('#keys-list')?.textContent.includes('uck_AbCd1234')`, 'keys list');
  check((await text('#keys-count')) === '1 active of 10', 'keys: count of active keys beside the list', `keys count: ${await text('#keys-count')}`);
  await onePrimary('/keys');
  await click('#create-open');
  check(
    (await isShown('#create')) && !(await isShown('#create-open')) && (await evaluate(`document.activeElement?.id`)) === 'key-name',
    'create form opens, takes focus, and replaces the header primary',
    'create form did not open cleanly',
  );
  await onePrimary('/keys create');
  let keyPosts = 0;
  const createFrom = apiCalls.length;
  stubs['POST /keys'] = () =>
    ++keyPosts === 1
      ? err('reauth_required', 'Enter your password again to continue.', null, 401, 'authentication_error')
      : ((keysList = [NEW_KEY, KEY]), okBody({ ...NEW_KEY, secret: SECRET }, 201));
  stubs['POST /reauth'] = { status: 204 };
  await click('#create-form button[type=submit]');
  await fieldErrorShown('key-name', 'Give the key a name.', 'key without a name');
  await setValue('#key-name', 'ci');
  await click('#create-form button[type=submit]');
  await waitFor(`document.querySelector('#reauth-dialog')?.open === true`, 'reauth dialog on reauth_required');
  check((await evaluate(`document.activeElement?.id`)) === 'reauth_password', 'reauth dialog focuses the password', 'reauth dialog did not focus the password');
  await shot('keys-reauth');
  await setValue('#reauth_password', 'my current passphrase');
  await click('#reauth-form button[type=submit]');
  await waitFor(`!document.querySelector('#reveal').hidden`, 'the new key to be revealed');
  check((await text('#reveal-secret')) === SECRET, 'new key shown in full after reauth', `revealed: ${await text('#reveal-secret')}`);
  const reauthCall = apiCalls.slice(createFrom).find((c) => c.path === '/reauth');
  const keyCalls = apiCalls.slice(createFrom).filter((c) => c.method === 'POST' && c.path === '/keys');
  check(
    reauthCall?.body?.password === 'my current passphrase' && keyCalls.length === 2 && keyCalls[1].body.name === 'ci' && js(keyCalls[1].body.scopes) === js(['jobs', 'read']),
    'reauth_required → POST /reauth { password } → POST /keys retried { name, scopes }',
    `reauth flow calls: ${js(apiCalls.slice(createFrom).map((c) => `${c.method} ${c.path} ${js(c.body)}`))}`,
  );
  csrfChecked('create key', createFrom);
  check(
    (await text('#reveal-warning')).includes('only time') && (await evaluate(`document.querySelector('#reveal-title') === document.activeElement`)),
    'reveal: one-time warning, focus on its heading',
    'reveal: warning or focus missing',
  );
  await waitFor(`document.querySelector('#keys-list')?.textContent.includes('uck_01234567')`, 'list to show the new key');
  check(!(await text('#keys-list')).includes(SECRET), 'the list shows the key prefix, never the secret', 'the secret appears in the keys list');
  await onePrimary('/keys reveal');
  await send('Browser.grantPermissions', { origin: appOrigin, permissions: ['clipboardReadWrite', 'clipboardSanitizedWrite'] }).catch(() => {});
  await click('#reveal-copy');
  await waitFor(`document.querySelector('#reveal-status').textContent.length > 0`, 'copy confirmation');
  ok(`copy button announces: "${await text('#reveal-status')}"`);
  await shot('keys-reveal');
  await click('#reveal-done');
  check(
    !(await evaluate(`document.body.textContent.includes(${js(SECRET)})`)) && !(await isShown('#reveal')),
    'after "I\'ve saved it" the secret is gone from the page',
    'the secret is still in the page after "I\'ve saved it"',
  );
  await appLoad('/keys');
  await waitFor(`document.querySelector('#keys-list')?.textContent.includes('uck_01234567')`, 'keys list after reload');
  check(!(await evaluate(`document.documentElement.outerHTML.includes(${js(SECRET)})`)), 'the key is not shown again after a reload', 'the key reappeared after a reload');

  const revokeFrom = apiCalls.length;
  stubs['DELETE /keys/7'] = () => ((keysList = [NEW_KEY, { ...KEY, revoked_at: 1791002000 }]), { status: 204 });
  await click('button[data-revoke="7"]');
  await waitFor(`document.querySelector('#revoke-dialog')?.open === true`, 'revoke dialog');
  check((await text('#revoke-display')) === 'uck_AbCd1234', 'revoke dialog names the key', `revoke dialog names "${await text('#revoke-display')}"`);
  await shot('keys-revoke');
  await click('#revoke-confirm');
  await waitFor(`document.querySelector('#revoke-dialog').open === false && [...document.querySelectorAll('#keys-list tr')].some((tr) => tr.textContent.includes('uck_AbCd1234') && tr.textContent.includes('Revoked'))`, 'revoked key to read Revoked');
  check(!(await evaluate(`!!document.querySelector('button[data-revoke="7"]')`)), 'revoked key has no Revoke action', 'revoked key still offers Revoke');
  check(apiCalls.slice(revokeFrom).some((c) => c.method === 'DELETE' && c.path === '/keys/7'), 'revoke sends DELETE /keys/7', 'revoke did not send DELETE /keys/7');
  csrfChecked('revoke key', revokeFrom);
  keysList = [KEY];

  // --- Jobs: list, filter, detail with usage, events, logs, artifacts, cancel.
  await appLoad('/jobs');
  await waitFor(`document.querySelectorAll('#jobs-list tbody tr').length === 2`, 'jobs list');
  check((await text('#jobs-running')) === '2', 'jobs: focal count of running jobs', `jobs running: ${await text('#jobs-running')}`);
  await click('#filters .chip[data-status="running"]');
  await waitFor(`document.querySelector('#filters .chip[data-status="running"]').getAttribute('aria-pressed') === 'true'`, 'status chip pressed');
  const listCall = apiCalls.filter((c) => c.path === '/jobs').at(-1);
  check(new URL(listCall.url).searchParams.getAll('status').join() === 'running', 'status chip filters GET /jobs?status=running', `jobs filter url: ${listCall.url}`);
  check(
    (await evaluate(`getComputedStyle(document.querySelector('#filters .chip[aria-pressed="true"]')).backgroundColor`)) === 'rgb(14, 14, 14)',
    'selected chip is an ink fill',
    'selected chip is not ink',
  );
  await shot('jobs-desktop');

  await appLoad(`/jobs/${JOB_ID}`);
  await waitFor(`!document.querySelector('#job').hidden`, 'job detail');
  check(
    (await text('#job-settled')) === '0.86' && (await text('#job-sub')).includes('Running') && (await text('#job-usage')).includes('2.5400') && (await text('#job-details')).includes('a10g-24gb-x1'),
    'job detail: charged so far, status, hold and SKU',
    `job detail: ${await text('#job-settled')} / ${await text('#job-sub')}`,
  );
  await waitFor(`document.querySelector('#job-events')?.textContent.includes('job.placed')`, 'job events');
  await waitFor(`document.querySelector('#job-logs pre')?.textContent.includes('step 2 of 9')`, 'job logs');
  await waitFor(`!!document.querySelector('#job-artifacts a[download]')`, 'artifact link');
  check(
    (await evaluate(`document.querySelector('#job-artifacts a[download]').href`)) === ARTIFACT_URL &&
      (await evaluate(`document.querySelector('#job-artifacts a[download]').rel`)).includes('noreferrer'),
    'artifacts: download link to the presigned URL, no referrer',
    'artifacts: download link wrong',
  );
  await shot('job-desktop');
  const cancelFrom = apiCalls.length;
  stubs[`POST /jobs/${JOB_ID}/cancel`] = okBody(CANCELLED, 202);
  await click('#job-cancel');
  await waitFor(`document.querySelector('#cancel-dialog')?.open === true`, 'cancel dialog');
  await click('#cancel-confirm');
  await waitFor(`document.querySelector('#job-sub').textContent.includes('Cancelled')`, 'job to read Cancelled');
  check(
    !(await isShown('#job-cancel')) && (await text('#job-status-line')).startsWith('Cancel requested'),
    'cancel: POST accepted, job shows Cancelled, no second cancel offered',
    'cancel: job page did not update',
  );
  csrfChecked('cancel job', cancelFrom);

  // A compute-api older than A7d has no dashboard artifacts route: say where
  // the files are.
  delete stubs[`GET /jobs/${JOB_ID}/artifacts`];
  stubs[`GET /jobs/${JOB_ID}`] = okBody(RUNNING);
  await appLoad(`/jobs/${JOB_ID}`);
  await waitFor(`document.querySelector('#job-artifacts')?.textContent.includes('/compute/v1/jobs/${JOB_ID}/artifacts')`, 'artifacts placeholder naming the job API route');
  ok('artifacts without the dashboard route (pre-A7d compute-api) point at the job API');
  stubs[`GET /jobs/job_missing`] = notFound;
  await appLoad('/jobs/job_missing');
  await formErrorShown("We couldn't find that job in your account.", 'unknown job');

  // --- Usage and Billing.
  stubs = baseStubs();
  await appLoad('/usage');
  await waitFor(`document.querySelector('#usage-total')?.textContent === '3.40'`, 'usage total');
  const now = new Date();
  const month = `${now.getUTCFullYear()}-${String(now.getUTCMonth() + 1).padStart(2, '0')}`;
  check(new URL(apiCalls.filter((c) => c.path === '/usage').at(-1).url).searchParams.get('month') === month, `usage asks for ${month}`, 'usage month parameter wrong');
  check(await evaluate(`document.querySelector('#month-next').disabled`), 'usage: no next month past the current one', 'usage: next month enabled for the future');
  await click('#month-prev');
  await waitFor(`!document.querySelector('#month-next').disabled`, 'previous month');
  await shot('usage-desktop');

  await appLoad('/billing');
  await waitFor(`document.querySelector('#history')?.textContent.includes('Charge')`, 'balance history');
  check(
    (await text('#balance-available')) === '12.50' && (await evaluate(`!!document.querySelector('#history a[href="/jobs/${JOB_ID}"]')`)),
    'billing: balance, history with job links',
    'billing: balance or history wrong',
  );
  check(
    (await evaluate(`[...document.querySelectorAll('#history tbody tr')].map((tr) => tr.cells[1].textContent + '|' + tr.cells[4].textContent).join()`)) ===
      `Charge|${JOB_ID},Hold|${JOB_ID},Credit|—`,
    'billing: each entry names its job; an operator credit shows no reference',
    'billing: history rows or references wrong',
  );
  await waitFor(`!document.querySelector('#topup-open').hidden`, 'billing: "Add credit" once top-ups are enabled');
  check(
    (await isShown('#topup-open')) && !(await isShown('#contact-primary')) && !(await isShown('#topup-off')) && !(await isShown('#topup')),
    'billing: the focal primary is "Add credit"; the contact card is gone while top-ups work',
    'billing: focal action or cards wrong with top-ups enabled',
  );
  check(
    (await evaluate(`[...document.querySelectorAll('#topups tbody tr')].map((tr) => [...tr.cells].map((c) => c.textContent).slice(1, 4).join('|')).join()`)) ===
      '15.00|Added|View receipt' &&
      (await evaluate(`document.querySelector('#topups a')?.getAttribute('href')`)) === TOPUP_OLD.receipt_url,
    'billing: top-ups listed with status and Stripe receipt link',
    `billing: top-up rows: ${await evaluate(`document.querySelector('#topups')?.textContent`)}`,
  );
  await onePrimary('/billing');
  await shot('billing-desktop');

  // --- Top-up: amount, Stripe Checkout (a full redirect), back, credited.
  await click('#topup-open');
  check(
    (await isShown('#topup')) && !(await isShown('#topup-open')) && (await evaluate(`document.activeElement?.id`)) === 'amount_usd',
    'top-up: "Add credit" opens the form, focuses the amount and steps aside for the form\'s primary',
    'top-up: form did not open or focus the amount',
  );
  await onePrimary('/billing form');
  await setValue('#amount_usd', '5');
  await click('#topup-form button[type=submit]');
  await fieldErrorShown('amount_usd', 'Enter an amount from USD 10.00 to 100.00.', 'top-up: below the minimum, checked in the page');
  stubs['POST /topups'] = err('topup_limit_exceeded', 'That would take your balance over USD 200.00, the most an account can hold. You can add up to USD 15.00 now.', 'amount_usd', 403, 'permission_error');
  await setValue('#amount_usd', '50');
  await click('#topup-form button[type=submit]');
  await fieldErrorShown('amount_usd', 'That would take your balance over USD 200.00, the most an account can hold. You can add up to USD 15.00 now.', 'top-up: topup_limit_exceeded under the amount');
  let polls = 0;
  stubs['POST /topups'] = okBody({ id: TOPUP_ID, object: 'compute.topup', status: 'open', checkout_url: 'https://checkout.stripe.com/c/pay/cs_test_b1' }, 201);
  stubs[`GET /topups/${TOPUP_ID}`] = () => okBody(++polls < 2 ? TOPUP_NEW : { ...TOPUP_NEW, status: 'credited', paid_at: 1791001020 });
  checkoutReturn = `${appOrigin}/billing?topup=${TOPUP_ID}&checkout=success`;
  const topupFrom = apiCalls.length;
  await setValue('#amount_usd', '20');
  await click('#topup-form button[type=submit]');
  await waitFor(`document.querySelector('#topup-status')?.textContent === 'USD 20.00 added to your balance.'`, 'top-up: credited after the return from Checkout', 15_000);
  const created = apiCalls.slice(topupFrom).find((c) => c.method === 'POST' && c.path === '/topups');
  check(
    created?.body?.amount_usd === '20.00' && checkoutVisits.at(-1) === 'https://checkout.stripe.com/c/pay/cs_test_b1' && polls >= 2,
    'top-up: POST /topups { amount_usd: "20.00" }, a full redirect to Checkout, then polled until credited',
    `top-up: body ${js(created?.body)}, checkout ${checkoutVisits.at(-1)}, polls ${polls}`,
  );
  csrfChecked('top-up', topupFrom);
  check((await evaluate('location.pathname + location.search')) === '/billing', 'top-up: the return URL is cleaned to /billing', `top-up: left at ${await evaluate('location.href')}`);
  await shot('billing-topup-credited');

  // Back from a cancelled Checkout: nothing charged, nothing polled.
  const backFrom = apiCalls.length;
  await appLoad(`/billing?topup=${TOPUP_ID}&checkout=cancelled`);
  await waitFor(`document.querySelector('#topup-status')?.textContent === 'Payment cancelled. Nothing was charged.'`, 'top-up: cancelled return');
  check(
    !apiCalls.slice(backFrom).some((c) => c.path === `/topups/${TOPUP_ID}`),
    'top-up: a cancelled return says nothing was charged and polls nothing',
    'top-up: a cancelled return polled the top-up',
  );
  delete stubs[`GET /topups/${TOPUP_ID}`];
  delete stubs['POST /topups'];

  // A deployment without Stripe keys: the contact route stays.
  stubs['GET /topups'] = topupList([], false);
  await appLoad('/billing');
  await waitFor(`document.querySelector('#history')?.textContent.includes('Charge')`, 'balance history without top-ups');
  check(
    (await evaluate(`document.querySelector('.page-head a.primary')?.getAttribute('href')`))?.startsWith('mailto:contactus@felixsphere.com') &&
      (await isShown('#topup-off')) && !(await isShown('#topup-open')) && !(await isShown('#topups-section')),
    'billing without top-ups: the one primary is "Contact us"',
    'billing without top-ups: no contact primary',
  );
  await onePrimary('/billing without top-ups');
  stubs = baseStubs();

  // A compute-api older than A7d answers 404 not_found: placeholders.
  for (const route of ['GET /balance', 'GET /transactions', 'GET /usage', 'GET /keys', 'GET /jobs', 'GET /topups']) delete stubs[route];
  for (const [path, sel, want] of [
    ['/billing', '#history', 'Your balance history will appear here'],
    ['/billing', '#topup-off', 'Card top-ups are coming'],
    ['/usage', '#usage-breakdown', 'Monthly usage will appear here'],
    ['/keys', '#keys-list', 'Your API keys will be listed here'],
    ['/jobs', '#jobs-list', 'Your jobs will be listed here'],
    ['/', '#balance-sub', 'Your balance will show here'],
  ]) {
    await appLoad(path);
    await waitFor(`document.querySelector(${js(sel)})?.textContent.includes(${js(want)})`, `${path} placeholder for a route not built yet`);
  }
  ok('every section has a placeholder while its route is not built');

  // --- Account: change password, log out.
  stubs = baseStubs();
  await appLoad('/account');
  await waitFor(`document.querySelector('#me-email')?.textContent === 'jane.doe@example.com'`, 'account email');
  await setValue('#current_password', 'wrong current one');
  await setValue('#password', 'a new long passphrase');
  await setValue('#password_confirm', 'a new long passphrase!');
  await click('#form button[type=submit]');
  await fieldErrorShown('password_confirm', "Passwords don't match.", 'account: confirm mismatch');
  await setValue('#password_confirm', 'a new long passphrase');
  stubs['POST /password'] = err('invalid_credentials', 'Your current password is not correct.', 'current_password', 401, 'authentication_error');
  await click('#form button[type=submit]');
  await fieldErrorShown('current_password', 'Your current password is not correct.', 'account: wrong current password');
  check((await evaluate('location.pathname')) === '/account', 'a wrong current password (401) does not log you out', 'a 401 invalid_credentials redirected');
  const pwFrom = apiCalls.length;
  stubs['POST /password'] = { status: 204 };
  await setValue('#current_password', 'right current one');
  await click('#form button[type=submit]');
  await waitFor(`document.querySelector('#password-status').textContent.startsWith('Password changed')`, 'password changed');
  const pw = apiCalls.slice(pwFrom).find((c) => c.path === '/password');
  check(
    pw?.body?.current_password === 'right current one' && pw.body.password === 'a new long passphrase' && pw.body.password_confirm === 'a new long passphrase',
    'POST /password { current_password, password, password_confirm }',
    `POST /password body: ${js(pw?.body)}`,
  );
  csrfChecked('change password', pwFrom);
  await onePrimary('/account');
  await shot('account-desktop');
  const outFrom = apiCalls.length;
  // W8 dropped the Account page's own Log out; W9 put it in the header.
  await click('#logout');
  await waitFor(`location.pathname === '/login'`, 'log out from /account');
  csrfChecked('log out', outFrom);

  // --- 401 session_required anywhere goes to /login.
  stubs = { ...baseStubs(), 'GET /transactions': sessionGone };
  await appLoad('/billing');
  await waitFor(`location.pathname === '/login'`, 'a 401 from a data route to redirect to /login');
  stubs = { ...baseStubs(), 'GET /me': sessionGone };
  await appLoad(`/jobs/${JOB_ID}`);
  await waitFor(`location.pathname === '/login'`, 'a 401 from /me on a job page to redirect to /login');
  stubs = { ...baseStubs(), 'POST /keys': sessionGone };
  await appLoad('/keys');
  await waitFor(`document.querySelector('#keys-list')?.textContent.includes('uck_AbCd1234')`, 'keys list');
  await click('#create-open');
  await setValue('#key-name', 'late');
  await click('#create-form button[type=submit]');
  await waitFor(`location.pathname === '/login'`, 'a 401 session_required on POST to redirect to /login');
  ok('401 session_required on GET /me, a data route or a POST goes to /login');

  // --- Phone width: no sideways scroll, and the menu under the header.
  stubs = baseStubs();
  for (const path of ['/', '/keys', '/jobs', `/jobs/${JOB_ID}`, '/usage', '/billing', '/account']) {
    await noHorizontalScroll(path);
  }
  await page('Emulation.setDeviceMetricsOverride', { width: 375, height: 812, deviceScaleFactor: 2, mobile: true });
  await appLoad('/keys');
  await waitFor(`document.querySelector('#keys-list')?.textContent.includes('uck_AbCd1234')`, 'keys at 375');
  const closed = await evaluate(`(() => {
    const b = document.querySelector('#menu-button').getBoundingClientRect();
    return { w: b.width, h: b.height, nav: document.querySelector('#app-nav').getClientRects().length, mark: document.querySelector('.app-header .mark').getClientRects().length, brand: document.querySelector('.app-header .brand').getClientRects().length, header: document.querySelector('.app-header').getBoundingClientRect().height };
  })()`);
  check(
    closed.w === 44 && closed.h === 44 && closed.nav === 0 && closed.mark > 0 && closed.brand === 0 && closed.header === 64,
    '375: 64px header, mark alone, 44px menu button, sidebar collapsed',
    `375 shell: ${js(closed)}`,
  );
  await click('#menu-button');
  const open = await evaluate(`(() => {
    const n = document.querySelector('#app-nav').getBoundingClientRect();
    const a = document.querySelector('#app-nav a[aria-current="page"]');
    return { top: n.top, width: n.width, row: a.getBoundingClientRect().height, href: a.getAttribute('href'), bg: getComputedStyle(a).backgroundColor, expanded: document.querySelector('#menu-button').getAttribute('aria-expanded') };
  })()`);
  check(
    open.top === 64 && open.width === 375 && open.row === 48 && open.href === '/keys' && open.bg === 'rgb(14, 14, 14)' && open.expanded === 'true',
    '375: menu opens as a full-width panel under the header, 48px rows, current item in ink',
    `375 menu: ${js(open)}`,
  );
  await shot('keys-375-menu');
  await pressKey('Escape', 'Escape', 27);
  check(
    (await evaluate(`document.querySelector('#app-nav').getClientRects().length`)) === 0 && (await evaluate(`document.activeElement?.id`)) === 'menu-button',
    '375: Escape closes the menu and returns focus to its button',
    '375: Escape did not close the menu',
  );
  await page('Emulation.clearDeviceMetricsOverride');
}

async function appChecks() {
  await page('Network.enable');
  await page('Fetch.enable', {
    patterns: [
      { urlPattern: '*/dashboard/v1/*', requestStage: 'Request' },
      { urlPattern: 'https://checkout.stripe.com/*', requestStage: 'Request' },
    ],
  });

  // --- Sign up
  await appLoad('/signup');
  check(
    (await evaluate('document.title')) === 'Sign up — Unify Compute',
    `${APP_HOST}/signup served`,
    `${APP_HOST}/signup title is "${await evaluate('document.title')}"`,
  );
  // The tokens and the stylesheet are external files; under a CSP with no
  // 'unsafe-inline' this proves both loaded and applied (--u-paper).
  const bg = await evaluate('getComputedStyle(document.body).backgroundColor');
  check(bg === 'rgb(250, 250, 247)', `${APP_HOST} styles applied under its CSP`, `${APP_HOST} body background is ${bg}; the stylesheets did not apply`);
  check(
    await evaluate(`document.querySelector('.brand img')?.complete && document.querySelector('.brand img').naturalWidth > 0`),
    `${APP_HOST} lockup loaded`,
    `${APP_HOST} lockup image did not load`,
  );
  check(
    (await evaluate(`[...document.fonts].some((f) => f.family.includes('Instrument Sans') && f.status === 'loaded')`)) === true,
    `${APP_HOST} self-hosted fonts loaded`,
    `${APP_HOST} Instrument Sans did not load`,
  );
  check(
    (await evaluate(`document.querySelectorAll('#hs-script-loader, iframe, script[src*="cloudflare"]').length`)) === 0,
    `${APP_HOST} has no HubSpot, Turnstile or iframes`,
    `${APP_HOST} loads a third-party script or frame`,
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
  {
    const consent = await termsLinks('.legal a');
    const below = await termsLinks('.below a');
    check(
      consent.join() === `Terms and Conditions|${TERMS_URL}|` && below.join() === `Terms|${TERMS_URL}|`,
      `/signup Terms links go to ${TERMS_URL}, same tab`,
      `/signup Terms links: consent ${JSON.stringify(consent)}, footer ${JSON.stringify(below)}`,
    );
    check((await evaluate(deadTerms)) === 0, '/signup has no compute.unifyai.us/terms link', '/signup still links to compute.unifyai.us/terms');
  }
  await shot('signup-desktop');

  // Show/hide: a real button with a label that says what it will do.
  const toggle = 'button.toggle[aria-controls="password"]';
  const state = () =>
    evaluate(`[document.querySelector('#password').type, document.querySelector(${js(toggle)}).getAttribute('aria-pressed'), document.querySelector(${js(toggle)}).getAttribute('aria-label')].join('|')`);
  const before = await state();
  await click(toggle);
  const shown = await state();
  await click(toggle);
  const hidden = await state();
  check(
    before === 'password|false|Show password' && shown === 'text|true|Hide password' && hidden === before,
    'password show/hide toggle (aria-pressed, aria-label)',
    `password toggle states: ${before} -> ${shown} -> ${hidden}`,
  );
  check(
    (await evaluate(`document.querySelectorAll('input[type=password]').length`)) ===
      (await evaluate(`document.querySelectorAll('button.toggle').length`)),
    'every password field has a toggle',
    'a password field has no show/hide toggle',
  );

  // Honeypot: off-screen, out of the tab order and the accessibility tree.
  const hp = await evaluate(`(() => { const el = document.querySelector('#website'); const r = el.getBoundingClientRect(); return { right: r.right, tab: el.tabIndex, hidden: el.closest('[aria-hidden="true"]') !== null }; })()`);
  const { root } = await page('DOM.getDocument', { depth: 0 });
  const { nodeId } = await page('DOM.querySelector', { nodeId: root.nodeId, selector: '#website' });
  const { nodes } = await page('Accessibility.getPartialAXTree', { nodeId, fetchRelatives: false });
  check(
    hp.right <= 0 && hp.tab === -1 && hp.hidden && nodes.every((n) => n.ignored),
    'honeypot off-screen, tabindex -1, aria-hidden, ignored by the accessibility tree',
    `honeypot exposed: ${JSON.stringify(hp)}, ax ignored=${nodes.map((n) => n.ignored)}`,
  );

  // Keyboard-only: Tab walks the form in reading order and never lands on the honeypot.
  // W9: the signed-out top bar (brand, language picker, the other auth link) comes first.
  const order = await tabOrder(10);
  const want = ['#topbar-brand', '#locale-picker', '#topbar-login', '#email', '#password', 'toggle:password', '#password_confirm', 'toggle:password_confirm', 'button:Sign up'];
  check(
    want.every((w, i) => order[i] === w) && !order.includes('#website'),
    `/signup tab order: ${want.join(' → ')}`,
    `/signup tab order is ${order.join(' → ')}`,
  );

  // Confirm password: "Passwords don't match." on blur, gone once they agree.
  await setValue('#email', 'person@example.com');
  await setValue('#password', 'correct horse battery');
  await setValue('#password_confirm', 'correct horse batterx');
  await blur('#password_confirm');
  await fieldErrorShown('password_confirm', "Passwords don't match.", 'confirm on blur');
  await shot('signup-mismatch');
  await setValue('#password_confirm', 'correct horse battery');
  check(
    !(await isShown('#password_confirm-error')),
    'mismatch error clears when the passwords agree',
    'mismatch error stayed after the passwords agreed',
  );
  // ...and on submit, without calling the API.
  await setValue('#password_confirm', 'something else entirely');
  const callsBefore = apiCalls.length;
  await click('button[type=submit]');
  await fieldErrorShown('password_confirm', "Passwords don't match.", 'confirm on submit');
  check(apiCalls.length === callsBefore, 'a mismatch is not sent to the API', 'a mismatched signup was sent to the API');
  await setValue('#password_confirm', 'correct horse battery');

  // A server error with `param` lands under that field.
  stubs = { 'POST /signup': err('weak_password', 'Use a password of at least 12 characters.', 'password') };
  await click('button[type=submit]');
  await fieldErrorShown('password', 'Use a password of at least 12 characters.', 'weak_password from the API');

  stubs = { 'POST /signup': { status: 202, body: { status: 'verification_sent' } } };
  await click('button[type=submit]');
  await waitFor(`location.pathname === '/verify' && document.readyState === 'complete'`, 'signup to go to /verify');
  const signup = apiCalls.filter((c) => c.path === '/signup').at(-1);
  check(
    signup?.body?.email === 'person@example.com' &&
      signup.body.password === 'correct horse battery' &&
      signup.body.password_confirm === 'correct horse battery' &&
      signup.body.website === '' &&
      !('turnstile_token' in signup.body),
    'POST /signup body: email, password, password_confirm, empty website honeypot, no Turnstile token',
    `POST /signup body: ${JSON.stringify(signup?.body)}`,
  );

  // --- Verify: the address came along in sessionStorage, not in the URL.
  await waitFor(`document.querySelector('#lead strong')?.textContent === 'person@example.com'`, '/verify to name the address');
  check(!(await isShown('#email')), '/verify hides the email field when the address is known', '/verify shows the email field although the address is known');
  check(
    (await evaluate(`getComputedStyle(document.querySelector('#code')).fontFamily`)).includes('IBM Plex Mono'),
    '/verify code field in IBM Plex Mono',
    '/verify code field is not mono',
  );
  stubs = {
    'POST /verify': err('invalid_code', 'That code is not valid. Check the latest email or request a new code.'),
    'POST /verify/resend': { status: 202, body: { status: 'verification_sent' } },
  };
  // Typed through the input pipeline, so maxlength and the like apply: a
  // code pasted as "123 456" must arrive as six digits.
  await evaluate(`document.querySelector('#code').focus()`);
  await page('Input.insertText', { text: '123 456' });
  check((await evaluate(`document.querySelector('#code').value`)) === '123456', '/verify takes "123 456" as 6 digits', `/verify code field holds "${await evaluate(`document.querySelector('#code').value`)}" after "123 456"`);
  await setValue('#code', '12a3456');
  check((await evaluate(`document.querySelector('#code').value`)) === '123456', '/verify keeps digits only', '/verify code field accepts non-digits');
  await click('button[type=submit]');
  await fieldErrorShown('code', 'That code is not valid. Check the latest email or request a new code.', 'invalid_code');
  await shot('verify-invalid-code');
  await click('#resend');
  await waitFor(`document.querySelector('#status').textContent.length > 0`, 'resend confirmation');
  const resend = apiCalls.filter((c) => c.path === '/verify/resend').at(-1);
  check(
    resend?.body?.email === 'person@example.com' && resend.body.website === '' && (await evaluate(`document.querySelector('#resend').disabled`)),
    'resend posts the address and the empty honeypot, and waits before allowing another',
    `resend: ${JSON.stringify(resend?.body)}`,
  );
  stubs = {
    'POST /verify': { status: 200, body: SESSION },
    'GET /me': { status: 200, body: SESSION },
    'POST /logout': { status: 204 },
  };
  await click('button[type=submit]');
  await waitFor(`location.pathname === '/' && document.querySelector('#account-email')?.textContent === 'person@example.com'`, 'verify to sign in and land on /');
  ok('verified and landed on the overview');
  await shot('home-desktop');
  await click('#logout');
  await waitFor(`location.pathname === '/login' && document.readyState === 'complete'`, 'log out to reach /login');
  const logout = apiCalls.filter((c) => c.path === '/logout').at(-1);
  check(logout?.headers?.['X-CSRF-Token'] === 'csrf-1', 'log out sends X-CSRF-Token', `log out headers: ${JSON.stringify(logout?.headers)}`);

  // --- Signed out: / sends you to /login.
  stubs = { 'GET /me': err('session_required', 'Log in to continue.', null, 401, 'authentication_error') };
  await appLoad('/');
  await waitFor(`location.pathname === '/login'`, '/ without a session to redirect to /login');
  ok('/ without a session goes to /login');

  // --- Log in
  await appLoad('/login');
  await shot('login-desktop');
  check(
    (await evaluate(`document.querySelector('.label-row a')?.getAttribute('href')`)) === '/forgot-password',
    '"Forgot password?" on the password label row',
    '"Forgot password?" link missing from the label row',
  );
  stubs = { 'POST /login': err('invalid_credentials', 'That email and password do not match an account.', null, 401, 'authentication_error') };
  await setValue('#email', 'person@example.com');
  await setValue('#password', 'wrong password here');
  await click('button[type=submit]');
  await formErrorShown('That email and password do not match an account.', 'invalid_credentials');
  check(
    (await evaluate(`getComputedStyle(document.querySelector('#form-error')).borderTopColor`)) === DANGER,
    'form-level error box has the danger border',
    'form-level error box border is not --u-danger',
  );
  await shot('login-error');
  const login = apiCalls.filter((c) => c.path === '/login').at(-1);
  check(
    login?.body?.email === 'person@example.com' && login.body.website === '' && !('turnstile_token' in login.body),
    'POST /login body: email, password, empty website honeypot',
    `POST /login body: ${JSON.stringify(login?.body)}`,
  );
  // Keyboard only: Enter in the password field submits the form.
  stubs = { 'POST /login': err('email_unverified', 'Verify your email address before you log in.', null, 403, 'authentication_error') };
  await evaluate(`document.querySelector('#password').focus()`);
  for (const type of ['keyDown', 'keyUp'])
    await page('Input.dispatchKeyEvent', { type, key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, ...(type === 'keyDown' ? { text: '\r' } : {}) });
  await waitFor(`document.querySelector('#form-error a')?.getAttribute('href') === '/verify'`, 'email_unverified to link to /verify');
  ok('Enter submits /login; email_unverified links to /verify');

  // --- Forgot password
  await appLoad('/forgot-password');
  // A 429 shows the wait from Retry-After when the message does not say it.
  stubs = { 'POST /password/forgot': { ...err('rate_limited', 'Too many attempts.', null, 429, 'rate_limited'), headers: [{ name: 'Retry-After', value: '600' }] } };
  await setValue('#email', 'person@example.com');
  await click('button[type=submit]');
  await formErrorShown('Too many attempts. Try again in 10 minutes.', '429 with Retry-After');
  stubs = { 'POST /password/forgot': { status: 202, body: { status: 'reset_requested' } } };
  await click('button[type=submit]');
  await waitFor(`!document.querySelector('#sent').hidden && document.querySelector('#sent-email').textContent === 'person@example.com'`, 'forgot-password confirmation');
  const forgot = apiCalls.filter((c) => c.path === '/password/forgot').at(-1);
  check(
    forgot?.body?.email === 'person@example.com' && forgot.body.website === '',
    '/forgot-password confirms without saying whether the account exists; sends the empty honeypot',
    `POST /password/forgot body: ${JSON.stringify(forgot?.body)}`,
  );

  // --- Reset password: the token stays in the fragment, then in memory.
  const TOKEN = 'rT0k-3n_fragment-only-7f3a9c';
  const resetFrom = requests.length;
  await appLoad(`/reset-password#token=${TOKEN}`);
  check(
    (await evaluate('location.href')).endsWith('/reset-password') && (await evaluate('location.hash')) === '',
    'reset token dropped from the address bar',
    `reset page URL still carries the token: ${await evaluate('location.href')}`,
  );
  stubs = { 'POST /password/reset': err('invalid_token', 'This reset link is invalid or has expired.', 'token') };
  await setValue('#password', 'a brand new passphrase');
  await setValue('#password_confirm', 'a brand new passphrase');
  await click('button[type=submit]');
  await formErrorShown('This reset link is invalid or has expired.', 'invalid_token');
  check(
    (await evaluate(`document.querySelector('#form-error a')?.getAttribute('href')`)) === '/forgot-password',
    'invalid_token offers a new link',
    'invalid_token has no link to /forgot-password',
  );
  stubs = { 'POST /password/reset': { status: 200, body: { status: 'password_reset' } } };
  await click('button[type=submit]');
  await waitFor(`!document.querySelector('#done').hidden`, 'reset confirmation');
  await shot('reset-done');
  const reset = apiCalls.filter((c) => c.path === '/password/reset').at(-1);
  check(
    reset?.body?.token === TOKEN && reset.body.password_confirm === 'a brand new passphrase',
    'POST /password/reset carries the token in the body',
    `POST /password/reset body: ${JSON.stringify(reset?.body)}`,
  );
  const resetRequests = requests.slice(resetFrom);
  const leaks = resetRequests.filter(
    (r) => r.url.includes(TOKEN) || Object.entries(r.headers).some(([k, v]) => k.toLowerCase() === 'referer' && String(v).includes(TOKEN)),
  );
  check(leaks.length === 0, 'reset token in no request URL and no Referer', `reset token leaked: ${leaks.map((r) => r.url).join(', ')}`);
  // DevTools lists the header with an empty value when the policy withheld it.
  const referer = (r) => Object.entries(r.headers).find(([k]) => k.toLowerCase() === 'referer')?.[1] ?? '';
  const referred = resetRequests.filter((r) => referer(r) !== '');
  check(
    referred.length === 0,
    'no Referer on any app-host request (Referrer-Policy: no-referrer)',
    `Referer sent: ${referred.map((r) => `${r.url} (${referer(r)})`).join(', ')}`,
  );
  await appLoad('/reset-password');
  await formErrorShown('This reset link is incomplete.', 'reset without a token');

  // --- Phone width, and the 404.
  for (const path of ['/signup', '/login', '/verify', '/forgot-password', '/reset-password', '/'])
    await noHorizontalScroll(path);
  await appLoad('/does-not-exist');
  check((await text('h1')) === 'Page not found', `${APP_HOST} 404 page`, `${APP_HOST} 404 page not rendered`);

  await dashboardChecks();

  // Only what the app host's own documents asked for: the marketing pages
  // checked before this can still be finishing lazy chunk loads.
  const foreign = [
    ...new Set(
      requests
        .filter((r) => r.document?.startsWith(appOrigin))
        .map((r) => r.url)
        // The top-up's redirect to Stripe Checkout, answered by the stub
        // above; it is the one cross-origin navigation the app makes.
        .filter((u) => !u.startsWith(appOrigin) && !u.startsWith('data:') && !u.startsWith('https://checkout.stripe.com/')),
    ),
  ];
  const appRequests = requests.filter((r) => r.document?.startsWith(appOrigin)).length;
  check(appRequests > 0 && foreign.length === 0, `${APP_HOST}: no third-party requests (${appRequests} seen)`, `${APP_HOST} requested: ${foreign.join(', ')}`);
  await page('Fetch.disable');
}

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

  {
    const footerTerms = await termsLinks('footer a');
    check(
      footerTerms.join() === `Terms|${TERMS_URL}|`,
      `/ footer Terms links to ${TERMS_URL}, same tab`,
      `/ footer Terms links: ${JSON.stringify(footerTerms)}`,
    );
    check((await evaluate(deadTerms)) === 0, '/ has no compute.unifyai.us/terms link', '/ still links to compute.unifyai.us/terms');
    const line = await evaluate(COMPANY_LINE);
    check(line === BRAND_LINE, `/ footer reads "${BRAND_LINE}"`, `/ footer company line: ${JSON.stringify(line)}`);
  }
  await clickAndExpectSoftNav(FOOTER_LINK, 'footer "Privacy"');
  await back();
  await clickAndExpectSoftNav(COOKIE_LINK, 'cookie notice "Privacy Policy"');
  await back();
  check(
    (await evaluate(hubspotLoaders)) === 1,
    'HubSpot loader still present once after navigating',
    'HubSpot loader duplicated or lost by navigation',
  );

  // UI-STANDARD "Marketing header": Log in, then Sign up, on the right.
  await setViewport(1280, 900, false);
  await load('/');
  await waitFor(`!!document.querySelector(${JSON.stringify(COOKIE_LINK)})`, 'hydration at 1280');
  for (const [href, text, cls] of [
    [LOGIN, 'Log in', '.nav-login'],
    [REGISTER, 'Sign up', '.button.primary'],
  ]) {
    const sel = `header .nav-actions a${cls}[href="${href}"]`;
    check(
      (await evaluate(visible(sel))) &&
        (await evaluate(`document.querySelector(${JSON.stringify(sel)}).textContent.trim()`)) === text,
      `1280: header "${text}" visible → ${href}`,
      `1280: header "${text}" link to ${href} missing or not visible`,
    );
  }
  check(
    await evaluate(visible('header .nav-inline a[href="/start"]')),
    '1280: header "Quick start" visible',
    '1280: header "Quick start" link missing or not visible',
  );
  await clickToStart('header .nav-inline a[href="/start"]', 'header "Quick start"');
  await back();
  // The account pages are on another host; the test asserts where the links
  // go rather than leaving the site under test.
  for (const [sel, text, label] of [
    [`.hero-actions a.button.primary[href="${REGISTER}"]`, 'Sign up', 'hero "Sign up"'],
    [`footer .footer-links a[href="${LOGIN}"]`, 'Log in', 'footer "Log in"'],
    [`footer .footer-links a[href="${REGISTER}"]`, 'Sign up', 'footer "Sign up"'],
  ]) {
    check(
      (await evaluate(`document.querySelector(${JSON.stringify(sel)})?.textContent.trim()`)) === text,
      `1280: ${label} → ${sel.match(/href="([^"]+)"/)[1]}`,
      `1280: ${label} link missing or mislabelled`,
    );
  }

  await setViewport(375, 812, true);
  await load('/');
  await waitFor(`!!document.querySelector(${JSON.stringify(COOKIE_LINK)})`, 'hydration at 375');
  check(
    !(await evaluate(visible(`header .nav-actions a.button.primary`))),
    '375: header Sign up collapsed into the menu',
    '375: header Sign up still shown outside the menu',
  );
  check(
    (await evaluate('document.documentElement.scrollWidth')) <= 375,
    '375: no horizontal scroll',
    `375: page is ${await evaluate('document.documentElement.scrollWidth')}px wide`,
  );
  await evaluate(`document.querySelector('.menu-button').click()`);
  if (await waitFor(visible(`#site-menu a.menu-signup[href="${REGISTER}"]`), 'mobile menu to open')) {
    check(
      await evaluate(visible(`#site-menu a.menu-account:not(.menu-signup)[href="${LOGIN}"]`)),
      `375: menu "Log in" visible → ${LOGIN}`,
      '375: menu "Log in" missing or not visible',
    );
    check(
      await evaluate(
        `getComputedStyle(document.querySelector('#site-menu a.menu-signup')).backgroundColor === 'rgb(245, 166, 35)'`,
      ),
      `375: menu "Sign up" is the amber primary → ${REGISTER}`,
      '375: menu "Sign up" is not styled as the primary button',
    );
    await clickToStart('#site-menu a[href="/start"]', 'menu "Quick start"');
    check(
      await evaluate(`document.getElementById('site-menu').hidden`),
      'menu closed after navigating',
      'menu still open after navigating',
    );
    // The curl examples are the widest thing on the site; they must scroll
    // inside their block, not widen the page.
    check(
      (await evaluate('document.documentElement.scrollWidth')) <= 375,
      '375: /start has no horizontal scroll',
      `375: /start is ${await evaluate('document.documentElement.scrollWidth')}px wide`,
    );
  }
  await setViewport(1280, 900, false);

  await load('/start');
  check(
    (await evaluate(`document.querySelector('h1')?.textContent`)) === START_H1,
    '/start full page load',
    '/start full page load did not render the guide',
  );
  check(
    (await evaluate(`[...document.querySelectorAll('.guide-steps h2')].some((h) => h.textContent === 'Create an account')`)) &&
      (await evaluate(`!!document.querySelector('#account a[href=${JSON.stringify(REGISTER)}]')`)) &&
      (await evaluate(`!document.getElementById('early-access')`)),
    '/start step 02 is "Create an account", linking to sign-up',
    '/start step 02 is not "Create an account" with a sign-up link, or #early-access is still there',
  );
  for (const copy of ['uck_', '403 compute_not_enabled', '503 upstream_unavailable', 'read -rs UNIFY_KEY']) {
    check(
      await evaluate(`document.querySelector('.guide').textContent.includes(${JSON.stringify(copy)})`),
      `/start says "${copy}"`,
      `/start does not say "${copy}"`,
    );
  }
  check(
    !(await evaluate(`/not open yet|Self-serve sign-up is coming/.test(document.querySelector('.guide').textContent)`)),
    '/start no longer says accounts are closed',
    '/start still says self-serve accounts are not open',
  );

  await load('/privacy');
  check(
    (await evaluate(`document.querySelector('h1')?.textContent`)) ===
      'Privacy Policy',
    '/privacy full page load',
    '/privacy full page load did not render the policy',
  );
  {
    const line = await evaluate(COMPANY_LINE);
    const want = `${BRAND_LINE}\n${ENTITY_LINE}`;
    check(line === want, '/privacy footer names FelixSphere LLC under the brand line', `/privacy footer company line: ${JSON.stringify(line)}`);
  }

  await load('/does-not-exist');
  check(
    await evaluate(
      `document.body.innerText.includes('This page could not be found')`,
    ),
    '404 page',
    '404 page not rendered',
  );

  if (appLocal) await appChecks();
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
    // The stubbed API's 4xx answers are the point of those checks.
    !e.includes('/dashboard/v1/') &&
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
