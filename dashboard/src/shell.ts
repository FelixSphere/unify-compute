// Shared by the signed-in pages in pages/: API types, requests, DOM helpers,
// the app shell and the reauth prompt.

import { $, type ApiError, busy, call, clearFieldError, GENERIC, input, type Result, setFieldError, setFormError } from './lib';

export type Me = {
  user: { email: string; created_at: number };
  account: { id: number; status: 'active' | 'suspended' | 'closed'; compute_enabled: boolean };
  csrf_token: string;
};
export type Balance = {
  available_usd: string;
  held_usd: string;
  currency?: 'USD';
  limits?: { topup_min_usd: string; topup_max_usd: string; balance_max_usd: string; topups_per_day: number };
};
export type Job = {
  id: string;
  status: string;
  sku: string;
  created_at: number;
  started_at: number | null;
  finished_at: number | null;
  spec?: { image?: string; command?: string[]; max_runtime_seconds?: number };
  usage?: { gpu_seconds: number; estimated_cost_usd: string; settled_cost_usd: string; hold_usd: string };
  error?: { code: string; message: string } | null;
  placement?: { cloud: string; region: string; instance_type: string } | null;
  metadata?: Record<string, string>;
};
export type Mount = { mount: string; description: string };
export type Template = {
  id: string;
  title: string;
  description: string;
  image: string;
  command: string[];
  env: Record<string, string>;
  sku: string;
  max_runtime_seconds: number;
  inputs: Mount[];
  outputs: Mount[];
  overridable: string[];
  available: boolean;
  unavailable_reason?: string;
  publisher?: string;
};
export type List<T> = { data: T[]; has_more?: boolean; next?: string };

export const CONTACT = 'mailto:contactus@felixsphere.com?subject=Unify%20Compute%20access';

// ---------------------------------------------------------------- requests

// The session is gone: the page has nothing to show. Callers never see the
// result, so nothing half-renders while the browser leaves.
export function toLogin<T>(): Promise<T> {
  location.replace('/login');
  return new Promise(() => {});
}

export async function get<T>(path: string): Promise<Result<T>> {
  const r = await call<T>('GET', path);
  if (!r.ok && r.error.status === 401) return toLogin();
  return r;
}

// Every state-changing request carries the CSRF token fetched from GET /me
// just before it, so a token rotated by a log-in in another tab, or a
// session that ended, is noticed here rather than as a 403.
export async function send<T>(method: 'POST' | 'PATCH' | 'DELETE', path: string, body?: unknown): Promise<Result<T>> {
  const m = await get<Me>('/me');
  if (!m.ok) return m;
  const r = await call<T>(method, path, body, m.data.csrf_token);
  if (!r.ok && r.error.code === 'session_required') return toLogin();
  return r;
}

// The one route that answers text/plain (job-api.md, logs).
export async function getText(path: string): Promise<Result<string>> {
  let res: Response;
  try {
    res = await fetch('/dashboard/v1' + path, {
      headers: { Accept: 'text/plain' },
      credentials: 'same-origin',
      cache: 'no-store',
    });
  } catch {
    return { ok: false, error: { status: 0, code: 'network', message: "We couldn't reach Unify Compute. Check your connection and try again." } };
  }
  if (res.status === 401) return toLogin();
  if (res.ok) return { ok: true, status: res.status, data: await res.text() };
  const e = (await res.json().catch(() => null))?.error;
  return { ok: false, error: { status: res.status, code: e?.code ?? 'unexpected', message: e?.message || GENERIC } };
}

export const listOf = <T>(d: T[] | List<T>): List<T> => (Array.isArray(d) ? { data: d } : d);
// compute-api answers 404 not_found for a /dashboard/v1 route it does not
// register; 501 is the job API's "not configured in this deployment".
export const notBuilt = (e: ApiError) => (e.status === 404 && e.code === 'not_found') || e.status === 501;

// ---------------------------------------------------------------- DOM

export type Child = Node | string | null | undefined | false;
export function h<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  attrs: Record<string, string | boolean | undefined> = {},
  ...children: Child[]
): HTMLElementTagNameMap[K] {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === undefined || v === false) continue;
    if (k === 'class') el.className = String(v);
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const c of children) if (c !== null && c !== undefined && c !== false) el.append(c);
  return el;
}

export const mono = (text: string) => h('span', { class: 'mono' }, text);
export const muted = (text: string, ...more: Child[]) => h('p', { class: 'muted' }, text, ...more);
export const fill = (id: string, ...nodes: Child[]) => {
  const el = $(id);
  if (el) el.replaceChildren(...nodes.filter((n): n is Node | string => !!n));
};
export const setText = (id: string, text: string) => {
  const el = $(id);
  if (el) el.textContent = text;
};

export function table(label: string, head: { text: string; num?: boolean }[], rows: Child[][]): HTMLElement {
  // Wide tables scroll inside this box at phone width; the page never does.
  // Focusable so the scroll works from the keyboard.
  return h(
    'div',
    { class: 'table-wrap', role: 'region', 'aria-label': label, tabindex: '0' },
    h(
      'table',
      {},
      h('thead', {}, h('tr', {}, ...head.map((c) => h('th', { scope: 'col', class: c.num ? 'num' : undefined }, c.text)))),
      h('tbody', {}, ...rows.map((r) => h('tr', {}, ...r.map((cell, i) => h('td', { class: head[i]?.num ? 'num' : undefined }, cell))))),
    ),
  );
}

export type Tone = 'success' | 'live' | 'danger' | 'warning' | 'muted';
export const state = (word: string, tone: Tone) => h('span', { class: 'state' }, h('span', { class: `dot dot-${tone}`, 'aria-hidden': 'true' }), word);

export const JOB_STATUS: Record<string, [string, Tone]> = {
  queued: ['Queued', 'muted'],
  provisioning: ['Provisioning', 'live'],
  running: ['Running', 'live'],
  succeeded: ['Succeeded', 'success'],
  failed: ['Failed', 'danger'],
  interrupted: ['Interrupted', 'warning'],
  cancelled: ['Cancelled', 'muted'],
};
export const jobState = (s: string) => state(...(JOB_STATUS[s] ?? [s, 'muted']));

export const dateFmt = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' });
export const when = (t: number | null | undefined) => (t ? dateFmt.format(new Date(t * 1000)) : '—');

export function duration(secs: number): string {
  const s = Math.max(0, Math.floor(secs));
  const hh = Math.floor(s / 3600);
  const mm = Math.floor((s % 3600) / 60);
  const ss = s % 60;
  if (hh) return `${hh}h ${mm}m`;
  if (mm) return `${mm}m ${ss}s`;
  return `${ss}s`;
}

export const jobLink = (id: string) => h('a', { href: `/jobs/${encodeURIComponent(id)}`, class: 'mono' }, id);

// ---------------------------------------------------------------- shell

export function initials(email: string): string {
  const local = email.split('@')[0] ?? '';
  const parts = local.split(/[._+-]+/).filter(Boolean);
  const s = parts.length > 1 ? parts[0][0] + parts[1][0] : local.slice(0, 1);
  return (s || '?').toUpperCase();
}

export async function logout(button: HTMLButtonElement) {
  button.disabled = true;
  const r = await send('POST', '/logout');
  if (r.ok || r.error.status === 401) {
    location.assign('/login');
    return;
  }
  button.disabled = false;
  setFormError(r.error.message);
}

export function wireMenus() {
  const menu = $<HTMLButtonElement>('menu-button')!;
  const nav = $('app-nav')!;
  const setMenu = (open: boolean) => {
    menu.setAttribute('aria-expanded', String(open));
    menu.setAttribute('aria-label', open ? 'Close menu' : 'Menu');
    nav.toggleAttribute('data-open', open);
  };
  menu.addEventListener('click', () => setMenu(menu.getAttribute('aria-expanded') !== 'true'));

  const account = $<HTMLButtonElement>('account-button')!;
  const panel = $('account-panel')!;
  const setAccount = (open: boolean) => {
    account.setAttribute('aria-expanded', String(open));
    panel.hidden = !open;
  };
  account.addEventListener('click', () => setAccount(account.getAttribute('aria-expanded') !== 'true'));
  document.addEventListener('click', (e) => {
    if (!panel.hidden && !(e.target as Element).closest('.account-menu')) setAccount(false);
  });
  document.addEventListener('keydown', (e) => {
    if (e.key !== 'Escape') return;
    if (!panel.hidden) {
      setAccount(false);
      account.focus();
    } else if (menu.getAttribute('aria-expanded') === 'true') {
      setMenu(false);
      menu.focus();
    }
  });
  const out = $<HTMLButtonElement>('logout')!;
  out.addEventListener('click', () => void logout(out));
}

export async function shell(): Promise<Me | null> {
  wireMenus();
  const r = await get<Me>('/me');
  if (!r.ok) {
    setFormError(r.error.message);
    return null;
  }
  setText('account-initials', initials(r.data.user.email));
  setText('account-email', r.data.user.email);
  $('account-button')?.setAttribute('aria-label', `Account: ${r.data.user.email}`);
  return r.data;
}

// ---------------------------------------------------------------- reauth

// Resolves true once POST /reauth succeeded, false if the person cancelled.
export function reauth(): Promise<boolean> {
  const dialog = $<HTMLDialogElement>('reauth-dialog')!;
  const form = $<HTMLFormElement>('reauth-form')!;
  const field = input('reauth_password')!;
  field.value = '';
  clearFieldError('reauth_password');
  return new Promise((resolve) => {
    const done = (ok: boolean) => {
      form.removeEventListener('submit', submit);
      cancelButton.removeEventListener('click', cancel);
      dialog.removeEventListener('cancel', cancel);
      field.value = '';
      if (dialog.open) dialog.close();
      resolve(ok);
    };
    const cancel = (e: Event) => {
      e.preventDefault();
      done(false);
    };
    const submit = async (e: SubmitEvent) => {
      e.preventDefault();
      if (!field.value) {
        setFieldError('reauth_password', 'Enter your password.');
        field.focus();
        return;
      }
      busy(form, true);
      const r = await send('POST', '/reauth', { password: field.value });
      busy(form, false);
      if (r.ok) return done(true);
      setFieldError('reauth_password', r.error.message);
      field.focus();
    };
    const cancelButton = $<HTMLButtonElement>('reauth-cancel')!;
    form.addEventListener('submit', submit);
    cancelButton.addEventListener('click', cancel);
    dialog.addEventListener('cancel', cancel);
    dialog.showModal();
    field.focus();
  });
}

// A sensitive action more than 10 minutes after the password was last
// entered answers 401 reauth_required; ask once, then try again.
export async function withReauth<T>(action: () => Promise<Result<T>>): Promise<Result<T> | null> {
  const r = await action();
  if (r.ok || r.error.code !== 'reauth_required') return r;
  if (!(await reauth())) return null;
  return action();
}

// ---------------------------------------------------------------- balance

export async function showBalance(): Promise<Balance | null> {
  const r = await get<Balance>('/balance');
  if (r.ok) {
    setText('balance-available', r.data.available_usd);
    setText(
      'balance-sub',
      /^0(\.0+)?$/.test(r.data.held_usd)
        ? 'Nothing is held for running jobs.'
        : `USD ${r.data.held_usd} more is held for running jobs and comes back when they finish.`,
    );
    return r.data;
  } else if (notBuilt(r.error)) {
    setText('balance-sub', 'Your balance will show here once billing is live in the dashboard.');
  } else {
    setText('balance-sub', r.error.message);
  }
  return null;
}

export function jobsTable(label: string, jobs: Job[]) {
  return table(
    label,
    [{ text: 'Job' }, { text: 'Status' }, { text: 'SKU' }, { text: 'Created' }, { text: 'GPU time', num: true }, { text: 'Charged (USD)', num: true }],
    jobs.map((j) => [
      jobLink(j.id),
      jobState(j.status),
      mono(j.sku),
      when(j.created_at),
      mono(duration(j.usage?.gpu_seconds ?? 0)),
      mono(j.usage?.settled_cost_usd ?? '—'),
    ]),
  );
}

// Same rule as compute-api (internal/payments ParseAmount): dollars with at
// most two decimals.
export function cents(usd: string): number | null {
  const m = /^(0|[1-9]\d{0,6})(?:\.(\d{1,2}))?$/.exec(usd.trim());
  return m ? Number(m[1]) * 100 + Number((m[2] ?? '').padEnd(2, '0')) : null;
}
