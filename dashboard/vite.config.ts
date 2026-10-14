import { copyFileSync, mkdirSync, readdirSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { defineConfig, type Plugin } from 'vite';
import { COMPUTE_API, CONTACT_EMAIL, TERMS_URL } from '../app/links';

// The app host's pages (app.compute.unifyai.us), built apart from the
// marketing site's vinext export. vinext streams each page's RSC payload as
// inline <script> chunks, and the app host's CSP is `script-src 'self'` with
// no 'unsafe-inline' and no hashes (deploy/site/Caddyfile). A plain Vite
// multi-page build emits only external, content-hashed scripts and
// stylesheets, so the pages fit that policy as it stands.

const here = dirname(fileURLToPath(import.meta.url));
const repo = resolve(here, '..');
const outDir = resolve(repo, 'dist/app');

const pages = readdirSync(here).filter((f) => f.endsWith('.html'));

// Byte-identical copies of the kit's files the marketing site already ships;
// one source in public/, nothing duplicated in the repository.
const brandFiles = [
  'favicon.ico',
  'icon-v4.svg',
  'favicon-32-v4.png',
  'brand/unifyai-lockup-horizontal-black.svg',
  'brand/unifyai-mark-black.svg',
];

const PRIVACY_URL = 'https://compute.unifyai.us/privacy';
const QUICK_START_URL = 'https://compute.unifyai.us/start';
// mailto, not the marketing site's HubSpot form: the app host's CSP admits
// no third-party script (ADR-0013's reasoning in Unify Data, kept here).
const HELP_MAILTO = `mailto:${CONTACT_EMAIL}?subject=${encodeURIComponent('Unify Compute help')}`;

const ICON_ATTRS =
  'xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"';
// lucide Eye / EyeOff (ISC), as SVG markup: no icon runtime on these pages.
const EYE = `<svg class="icon-show" ${ICON_ATTRS}><path d="M2.062 12.348a1 1 0 0 1 0-.696 10.75 10.75 0 0 1 19.876 0 1 1 0 0 1 0 .696 10.75 10.75 0 0 1-19.876 0"/><circle cx="12" cy="12" r="3"/></svg>`;
const EYE_OFF = `<svg class="icon-hide" ${ICON_ATTRS}><path d="M10.733 5.076a10.744 10.744 0 0 1 11.205 6.575 1 1 0 0 1 0 .696 10.747 10.747 0 0 1-1.444 2.49"/><path d="M14.084 14.158a3 3 0 0 1-4.242-4.242"/><path d="M17.479 17.499a10.75 10.75 0 0 1-15.417-5.151 1 1 0 0 1 0-.696 10.75 10.75 0 0 1 4.446-5.143"/><path d="m2 2 20 20"/></svg>`;
// lucide CircleAlert (ISC).
const ALERT = `<svg ${ICON_ATTRS}><circle cx="12" cy="12" r="10"/><line x1="12" x2="12" y1="8" y2="12"/><line x1="12" x2="12.01" y1="16" y2="16"/></svg>`;
// lucide Menu / X (ISC), for the phone menu button (UI-STANDARD "App shell").
const MENU = `<svg class="icon-open" ${ICON_ATTRS}><line x1="4" x2="20" y1="12" y2="12"/><line x1="4" x2="20" y1="6" y2="6"/><line x1="4" x2="20" y1="18" y2="18"/></svg>`;
const CLOSE = `<svg class="icon-close" ${ICON_ATTRS}><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>`;
// lucide CircleHelp (ISC), for the header's Help menu.
const HELP = `<svg ${ICON_ATTRS}><circle cx="12" cy="12" r="10"/><path d="M9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3"/><path d="M12 17h.01"/></svg>`;

// The signed-in pages, in sidebar order. `path` is what the browser shows;
// /keys and /members are also where the mailer's "Review keys" and "View
// members" links point (compute-api/internal/mailer/).
const NAV: { id: string; path: string; label: string; key: string }[] = [
  { id: 'overview', path: '/', label: 'Overview', key: 'nav.overview' },
  { id: 'jobs', path: '/jobs', label: 'Jobs', key: 'nav.jobs' },
  { id: 'marketplace', path: '/marketplace', label: 'Marketplace', key: 'nav.marketplace' },
  { id: 'listings', path: '/listings', label: 'My listings', key: 'nav.listings' },
  { id: 'keys', path: '/keys', label: 'API keys', key: 'nav.keys' },
  { id: 'usage', path: '/usage', label: 'Usage', key: 'nav.usage' },
  { id: 'billing', path: '/billing', label: 'Billing', key: 'nav.billing' },
  { id: 'members', path: '/members', label: 'Members', key: 'nav.members' },
  { id: 'account', path: '/account', label: 'Account', key: 'nav.account' },
];
// lucide LogOut (ISC), beside the "Log out" label in the header.
const LOGOUT = `<svg ${ICON_ATTRS}><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><polyline points="16 17 21 12 16 7"/><line x1="21" x2="9" y1="12" y2="12"/></svg>`;

const partials: Record<string, (arg: string) => string> = {
  head: () => `<meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <meta name="robots" content="noindex" />
    <link rel="icon" href="/favicon.ico?v=4" sizes="any" />
    <link rel="icon" href="/icon-v4.svg" type="image/svg+xml" />
    <link rel="icon" href="/favicon-32-v4.png" sizes="32x32" type="image/png" />`,
  brand: () => `<div class="brand">
          <img src="/brand/unifyai-lockup-horizontal-black.svg" alt="UnifyAI" width="148" height="51" loading="eager" />
          <span class="divider" aria-hidden="true"></span>
          <span class="descriptor">Compute <span class="partner">&amp; Flatkey</span></span>
        </div>`,
  'form-error': () => `<div class="form-error" id="form-error" role="alert" tabindex="-1" hidden>${ALERT}<p></p></div>`,
  toggle: (id) =>
    `<button type="button" class="toggle" aria-controls="${id}" aria-label="Show password" aria-pressed="false" data-i18n-attr="aria-label:common.show-password">${EYE}${EYE_OFF}</button>`,
  // The honeypot the server checks on signup, resend, login and forgot
  // (ADR-0011 D5: no Turnstile). Off-screen rather than display:none, since
  // some bots skip fields that are not rendered; aria-hidden and tabindex=-1
  // keep it from people and screen readers.
  honeypot: () => `<div class="hp" aria-hidden="true">
            <label for="website" data-i18n="common.website">Website</label>
            <input id="website" name="website" type="text" tabindex="-1" autocomplete="off" />
          </div>`,
  // UI-STANDARD "App shell": 64px header (lockup + divider + "Compute", the
  // phone menu button, the account button) and the 216px sidebar, which is
  // also the phone menu's panel below 768px. The page supplies <main>.
  shell: (active) => {
    if (!NAV.some((n) => n.id === active)) throw new Error(`@shell: unknown page ${active}`);
    const items = NAV.map(
      (n) =>
        `<li><a href="${n.path}"${n.id === active ? ' aria-current="page"' : ''} data-i18n="${n.key}">${n.label}</a></li>`,
    ).join('');
    return `<a class="skip" href="#main" data-i18n="common.skip-to-content">Skip to content</a>
    <header class="app-header">
      <a class="brand" href="/" aria-label="Unify Compute overview" data-i18n-attr="aria-label:common.overview-link">
        <img src="/brand/unifyai-lockup-horizontal-black.svg" alt="UnifyAI" width="148" height="51" loading="eager" />
        <span class="divider" aria-hidden="true"></span>
        <span class="descriptor">Compute <span class="partner">&amp; Flatkey</span></span>
      </a>
      <a class="mark" href="/" aria-label="Unify Compute overview" data-i18n-attr="aria-label:common.overview-link"><img src="/brand/unifyai-mark-black.svg" alt="UnifyAI" width="32" height="32" loading="eager" /></a>
      <div class="header-actions">
        <div class="header-menu">
          <button class="help-button" type="button" id="help-button" aria-expanded="false" aria-controls="help-panel">${HELP}<span class="help-label" data-i18n="common.help">Help</span></button>
          <div class="account-panel" id="help-panel" hidden>
            <a href="${QUICK_START_URL}" target="_blank" rel="noopener" data-i18n="common.quick-start">Quick start</a>
            <a href="${COMPUTE_API}/openapi.json" target="_blank" rel="noopener" data-i18n="common.api-reference">API reference</a>
            <a href="${HELP_MAILTO}" data-i18n="common.contact-us">Contact us</a>
          </div>
        </div>
        <div class="locale" id="locale"></div>
        <button class="menu-button" type="button" id="menu-button" aria-label="Menu" aria-expanded="false" aria-controls="app-nav" data-i18n-attr="aria-label:common.menu">${MENU}${CLOSE}</button>
        <a class="identity" id="account-button" href="/account"><span class="avatar" aria-hidden="true"><span id="account-initials"></span></span><span class="identity-email" id="account-email"></span></a>
        <button type="button" class="logout-button" id="logout"><span data-i18n="common.log-out">Log out</span>${LOGOUT}</button>
      </div>
    </header>
    <nav class="app-nav" id="app-nav" aria-label="Dashboard" data-i18n-attr="aria-label:common.dashboard-nav">
      <ul>${items}</ul>
      <p class="nav-foot">app.compute.unifyai.us<br /><span data-i18n="common.nav-foot">GPU jobs, billed by the second</span></p>
    </nav>`;
  },
  // The password prompt for a sensitive action more than 10 minutes after the
  // password was last entered (401 reauth_required, POST /reauth).
  reauth: () => `<dialog id="reauth-dialog" aria-labelledby="reauth-title">
        <form id="reauth-form" novalidate>
          <h2 id="reauth-title" data-i18n="reauth.title">Confirm it's you</h2>
          <p data-i18n="reauth.lead">Enter your password to continue.</p>
          <div class="field">
            <label for="reauth_password" data-i18n="common.password">Password</label>
            <div class="password">
              <input id="reauth_password" name="password" type="password" autocomplete="current-password" required aria-describedby="reauth_password-error" />
              <button type="button" class="toggle" aria-controls="reauth_password" aria-label="Show password" aria-pressed="false" data-i18n-attr="aria-label:common.show-password">${EYE}${EYE_OFF}</button>
            </div>
            <p class="field-error" id="reauth_password-error" hidden></p>
          </div>
          <div class="actions">
            <button class="primary" type="submit" data-i18n="common.continue">Continue</button>
            <button class="ghost" type="button" id="reauth-cancel" data-i18n="common.cancel">Cancel</button>
          </div>
        </form>
      </dialog>`,
  legal: () =>
    `<p class="legal" data-i18n="signup.legal">By signing up you agree to the <a href="${TERMS_URL}">Terms and Conditions</a> and <a href="${PRIVACY_URL}">Privacy Policy</a>.</p>`,
  below: () => `<p class="below">© ${new Date().getFullYear()} UnifyAI · <a href="${PRIVACY_URL}" data-i18n="common.privacy">Privacy</a> · <a href="${TERMS_URL}" data-i18n="common.terms">Terms</a></p>`,
  // The signed-out pages' 64px top bar, as Unify Data's /app has: the brand,
  // the language picker, and Log in / Sign up at the right (UI-STANDARD
  // "Marketing header"), minus the link to the page you are on.
  topbar: (page) => `<header class="app-header topbar">
      <a class="brand" href="https://compute.unifyai.us/" aria-label="Unify Compute" data-i18n-attr="aria-label:common.site-link">
        <img src="/brand/unifyai-lockup-horizontal-black.svg" alt="UnifyAI" width="148" height="51" loading="eager" />
        <span class="divider" aria-hidden="true"></span>
        <span class="descriptor">Compute <span class="partner">&amp; Flatkey</span></span>
      </a>
      <a class="mark" href="https://compute.unifyai.us/" aria-label="Unify Compute" data-i18n-attr="aria-label:common.site-link"><img src="/brand/unifyai-mark-black.svg" alt="UnifyAI" width="32" height="32" loading="eager" /></a>
      <div class="header-actions">
        <div class="locale" id="locale"></div>
        ${page === 'login' ? '' : '<a class="topbar-login" href="/login" data-i18n="common.log-in">Log in</a>'}
        ${page === 'signup' ? '' : '<a class="primary topbar-signup" href="/signup" data-i18n="common.sign-up">Sign up</a>'}
      </div>
    </header>`,
};

function htmlPartials(): Plugin {
  return {
    name: 'unify-compute-partials',
    transformIndexHtml: {
      order: 'pre',
      handler: (html, ctx) => {
        const out = html.replace(/<!-- @([a-z-]+)(?::([a-z_-]+))? -->/g, (m, name: string, arg = '') => {
          const partial = partials[name];
          if (!partial) throw new Error(`${ctx.filename}: unknown partial ${m}`);
          return partial(arg);
        });
        if (out.includes('<!-- @')) throw new Error(`${ctx.filename}: malformed partial marker`);
        return out;
      },
    },
  };
}

function brandCopies(): Plugin {
  return {
    name: 'unify-compute-brand-files',
    apply: 'build',
    writeBundle() {
      for (const f of brandFiles) {
        mkdirSync(dirname(join(outDir, f)), { recursive: true });
        copyFileSync(join(repo, 'public', f), join(outDir, f));
      }
    },
  };
}

export default defineConfig({
  root: here,
  base: '/',
  publicDir: false,
  plugins: [htmlPartials(), brandCopies()],
  build: {
    outDir,
    emptyOutDir: true,
    // Every browser these pages support has modulepreload; skip the shim.
    modulePreload: { polyfill: false },
    rolldownOptions: {
      input: Object.fromEntries(pages.map((p) => [p.replace(/\.html$/, ''), resolve(here, p)])),
    },
  },
});
