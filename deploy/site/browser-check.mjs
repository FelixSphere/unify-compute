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

// The app host's pages (dashboard/, plan task A7b). compute-api is not in
// this container, so /dashboard/v1/* is answered here, in the browser, by
// stubs in the shapes of docs/spec/accounts.md; every call is recorded so the
// request bodies and headers can be checked. Every other request is recorded
// too, to prove nothing leaves the origin and the reset token stays out of
// URLs and Referer.
const apiCalls = [];
const requests = [];
let stubs = {};
listeners.add((msg) => {
  if (msg.sessionId !== sessionId) return;
  if (msg.method === 'Network.requestWillBeSent') {
    requests.push({ url: msg.params.request.url, headers: msg.params.request.headers });
  } else if (msg.method === 'Fetch.requestPaused') {
    const { requestId, request } = msg.params;
    const path = new URL(request.url).pathname.replace(/^\/dashboard\/v1/, '');
    let body;
    try {
      body = request.postData ? JSON.parse(request.postData) : undefined;
    } catch {
      body = request.postData;
    }
    apiCalls.push({ method: request.method, path, url: request.url, headers: request.headers, body });
    const stub = stubs[`${request.method} ${path}`] ?? {
      status: 404,
      body: { error: { type: 'not_found', code: 'not_found', message: 'Not found.' } },
    };
    send(
      'Fetch.fulfillRequest',
      {
        requestId,
        responseCode: stub.status,
        responseHeaders: [{ name: 'Content-Type', value: 'application/json' }],
        body: Buffer.from(JSON.stringify(stub.body ?? {})).toString('base64'),
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
const visible = (sel) =>
  evaluate(`(() => { const el = document.querySelector(${js(sel)}); return !!el && !el.closest('[hidden]') && el.getClientRects().length > 0; })()`);
const DANGER = 'rgb(217, 45, 32)';
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
  await shot(`${path === '/' ? 'home' : path.slice(1)}-375`);
  await page('Emulation.clearDeviceMetricsOverride');
};

async function appChecks() {
  await page('Network.enable');
  await page('Fetch.enable', { patterns: [{ urlPattern: '*/dashboard/v1/*', requestStage: 'Request' }] });

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
  const order = await tabOrder(7);
  const want = ['#email', '#password', 'toggle:password', '#password_confirm', 'toggle:password_confirm', 'button:Sign up'];
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
    !(await visible('#password_confirm-error')),
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
  check(!(await visible('#email')), '/verify hides the email field when the address is known', '/verify shows the email field although the address is known');
  check(
    (await evaluate(`getComputedStyle(document.querySelector('#code')).fontFamily`)).includes('IBM Plex Mono'),
    '/verify code field in IBM Plex Mono',
    '/verify code field is not mono',
  );
  stubs = {
    'POST /verify': err('invalid_code', 'That code is not valid. Check the latest email or request a new code.'),
    'POST /verify/resend': { status: 202, body: {} },
  };
  await setValue('#code', '12a3456');
  check((await evaluate(`document.querySelector('#code').value`)) === '123456', '/verify keeps digits only', '/verify code field accepts non-digits');
  await click('button[type=submit]');
  await fieldErrorShown('code', 'That code is not valid. Check the latest email or request a new code.', 'invalid_code');
  await shot('verify-invalid-code');
  await click('#resend');
  await waitFor(`document.querySelector('#status').textContent.length > 0`, 'resend confirmation');
  const resend = apiCalls.filter((c) => c.path === '/verify/resend').at(-1);
  check(
    resend?.body?.email === 'person@example.com' && (await evaluate(`document.querySelector('#resend').disabled`)),
    'resend posts the address and waits before allowing another',
    `resend: ${JSON.stringify(resend?.body)}`,
  );
  stubs = {
    'POST /verify': { status: 200, body: { user: { email: 'person@example.com' }, account: { id: 1 }, csrf_token: 'csrf-1' } },
    'GET /me': {
      status: 200,
      body: { user: { email: 'person@example.com', created_at: 0 }, account: { id: 1, status: 'active', compute_enabled: false }, csrf_token: 'csrf-1' },
    },
    'POST /logout': { status: 204 },
  };
  await click('button[type=submit]');
  await waitFor(`location.pathname === '/' && document.querySelector('#account-email')?.textContent === 'person@example.com'`, 'verify to sign in and land on /');
  ok('verified and landed on the signed-in placeholder');
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
  stubs = { 'POST /login': err('email_unverified', 'Verify your email address before you log in.', null, 403, 'authentication_error') };
  await click('button[type=submit]');
  await waitFor(`document.querySelector('#form-error a')?.getAttribute('href') === '/verify'`, 'email_unverified to link to /verify');
  ok('email_unverified links to /verify');

  // --- Forgot password
  await appLoad('/forgot-password');
  stubs = { 'POST /password/forgot': { status: 202, body: {} } };
  await setValue('#email', 'person@example.com');
  await click('button[type=submit]');
  await waitFor(`!document.querySelector('#sent').hidden && document.querySelector('#sent-email').textContent === 'person@example.com'`, 'forgot-password confirmation');
  ok('/forgot-password confirms without saying whether the account exists');

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
  stubs = { 'POST /password/reset': { status: 200, body: {} } };
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

  const foreign = [...new Set(requests.map((r) => r.url).filter((u) => !u.startsWith(appOrigin) && !u.startsWith('data:')))];
  check(foreign.length === 0, `${APP_HOST}: no third-party requests`, `${APP_HOST} requested: ${foreign.join(', ')}`);
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
