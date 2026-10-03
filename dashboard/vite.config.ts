import { copyFileSync, mkdirSync, readdirSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { defineConfig, type Plugin } from 'vite';

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

const ICON_ATTRS =
  'xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"';
// lucide Eye / EyeOff (ISC), as SVG markup: no icon runtime on these pages.
const EYE = `<svg class="icon-show" ${ICON_ATTRS}><path d="M2.062 12.348a1 1 0 0 1 0-.696 10.75 10.75 0 0 1 19.876 0 1 1 0 0 1 0 .696 10.75 10.75 0 0 1-19.876 0"/><circle cx="12" cy="12" r="3"/></svg>`;
const EYE_OFF = `<svg class="icon-hide" ${ICON_ATTRS}><path d="M10.733 5.076a10.744 10.744 0 0 1 11.205 6.575 1 1 0 0 1 0 .696 10.747 10.747 0 0 1-1.444 2.49"/><path d="M14.084 14.158a3 3 0 0 1-4.242-4.242"/><path d="M17.479 17.499a10.75 10.75 0 0 1-15.417-5.151 1 1 0 0 1 0-.696 10.75 10.75 0 0 1 4.446-5.143"/><path d="m2 2 20 20"/></svg>`;
// lucide CircleAlert (ISC).
const ALERT = `<svg ${ICON_ATTRS}><circle cx="12" cy="12" r="10"/><line x1="12" x2="12" y1="8" y2="12"/><line x1="12" x2="12.01" y1="16" y2="16"/></svg>`;

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
          <span class="descriptor">Compute</span>
        </div>`,
  'form-error': () => `<div class="form-error" id="form-error" role="alert" tabindex="-1" hidden>${ALERT}<p></p></div>`,
  toggle: (id) =>
    `<button type="button" class="toggle" aria-controls="${id}" aria-label="Show password" aria-pressed="false">${EYE}${EYE_OFF}</button>`,
  // The honeypot the server checks on signup, resend, login and forgot
  // (ADR-0011 D5: no Turnstile). Off-screen rather than display:none, since
  // some bots skip fields that are not rendered; aria-hidden and tabindex=-1
  // keep it from people and screen readers.
  honeypot: () => `<div class="hp" aria-hidden="true">
            <label for="website">Website</label>
            <input id="website" name="website" type="text" tabindex="-1" autocomplete="off" />
          </div>`,
  below: () => `<p class="below">© ${new Date().getFullYear()} UnifyAI · <a href="https://compute.unifyai.us/privacy">Privacy</a> · <a href="https://compute.unifyai.us/terms">Terms</a></p>`,
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
