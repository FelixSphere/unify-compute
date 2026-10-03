// The signed-in pages (UI-STANDARD "App shell"). Every route and shape is
// docs/spec/accounts.md, "Dashboard API"; compute-api serves all of them
// since A7d. An older compute-api answers 404 not_found for a route it does
// not register, and one without a job service 501; each section then says
// what will appear there instead of failing the page.

import { API, $, type ApiError, busy, call, checkConfirm, checkNewPassword, clearFieldError, focusFirstInvalid, GENERIC, input, onSubmit, type Result, setFieldError, setFormError, showApiError, wireConfirm } from './lib';

type Me = {
  user: { email: string; created_at: number };
  account: { id: number; status: 'active' | 'suspended' | 'closed'; compute_enabled: boolean };
  csrf_token: string;
};
type Balance = {
  available_usd: string;
  held_usd: string;
  currency?: 'USD';
  limits?: { topup_min_usd: string; topup_max_usd: string; balance_max_usd: string; topups_per_day: number };
};
type Topup = {
  id: string;
  amount_usd: string;
  status: string;
  created_at: number;
  paid_at: number | null;
  receipt_url: string | null;
  refunded_usd: string;
};
type TopupList = { data: Topup[]; topups_enabled?: boolean };
type Key = {
  id: number | string;
  name: string;
  display: string;
  scopes: string[];
  created_at: number;
  expires_at: number | null;
  last_used_at: number | null;
  revoked_at: number | null;
};
type Job = {
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
type JobEvent = { id: string; type: string; created_at: number; data: Record<string, unknown> };
type Artifact = { path: string; size: number; sha256: string; url: string; expires_at: number };
type Entry = {
  id: string;
  kind: string;
  amount_usd: string;
  available_after_usd: string;
  job_id?: string | null;
  ref: string | { type?: string; id?: string } | null;
  created_at: number;
};
type KeyRef = { id: number; name: string; display: string };
type Usage = {
  month?: string;
  from: string;
  to: string;
  jobs: number;
  gpu_seconds: number;
  gpu_usd: string;
  storage_usd: string;
  egress_usd: string;
  total_usd: string;
  by_sku: { sku: string; gpu_seconds: number; usd: string }[];
  by_day: { date: string; gpu_seconds: number; usd: string }[];
  by_key: { key: KeyRef; jobs: number; gpu_seconds: number; usd: string }[];
};
type JobUsage = {
  job_id: string;
  sku: string;
  status: string;
  key: KeyRef;
  started_at: number | null;
  finished_at: number | null;
  settled_at: number;
  gpu_seconds: number;
  usd: string;
};
type JobUsageList = { data: JobUsage[]; has_more: boolean; next?: string };
type List<T> = { data: T[]; has_more?: boolean; next?: string };

const CONTACT = 'mailto:contactus@felixsphere.com?subject=Unify%20Compute%20access';
const QUICK_START = 'https://compute.unifyai.us/start';
const TERMINAL = new Set(['succeeded', 'failed', 'interrupted', 'cancelled']);

// ---------------------------------------------------------------- requests

// The session is gone: the page has nothing to show. Callers never see the
// result, so nothing half-renders while the browser leaves.
function toLogin<T>(): Promise<T> {
  location.replace('/login');
  return new Promise(() => {});
}

async function get<T>(path: string): Promise<Result<T>> {
  const r = await call<T>('GET', path);
  if (!r.ok && r.error.status === 401) return toLogin();
  return r;
}

// Every state-changing request carries the CSRF token fetched from GET /me
// just before it, so a token rotated by a log-in in another tab, or a
// session that ended, is noticed here rather than as a 403.
async function send<T>(method: 'POST' | 'DELETE', path: string, body?: unknown): Promise<Result<T>> {
  const m = await get<Me>('/me');
  if (!m.ok) return m;
  const r = await call<T>(method, path, body, m.data.csrf_token);
  if (!r.ok && r.error.code === 'session_required') return toLogin();
  return r;
}

// The one route that answers text/plain (job-api.md, logs).
async function getText(path: string): Promise<Result<string>> {
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

const listOf = <T>(d: T[] | List<T>): List<T> => (Array.isArray(d) ? { data: d } : d);
// compute-api answers 404 not_found for a /dashboard/v1 route it does not
// register; 501 is the job API's "not configured in this deployment".
const notBuilt = (e: ApiError) => (e.status === 404 && e.code === 'not_found') || e.status === 501;

// ---------------------------------------------------------------- DOM

type Child = Node | string | null | undefined | false;
function h<K extends keyof HTMLElementTagNameMap>(
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

const mono = (text: string) => h('span', { class: 'mono' }, text);
const muted = (text: string, ...more: Child[]) => h('p', { class: 'muted' }, text, ...more);
const fill = (id: string, ...nodes: Child[]) => {
  const el = $(id);
  if (el) el.replaceChildren(...nodes.filter((n): n is Node | string => !!n));
};
const setText = (id: string, text: string) => {
  const el = $(id);
  if (el) el.textContent = text;
};

function table(label: string, head: { text: string; num?: boolean }[], rows: Child[][]): HTMLElement {
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

type Tone = 'success' | 'live' | 'danger' | 'warning' | 'muted';
const state = (word: string, tone: Tone) => h('span', { class: 'state' }, h('span', { class: `dot dot-${tone}`, 'aria-hidden': 'true' }), word);

const JOB_STATUS: Record<string, [string, Tone]> = {
  queued: ['Queued', 'muted'],
  provisioning: ['Provisioning', 'live'],
  running: ['Running', 'live'],
  succeeded: ['Succeeded', 'success'],
  failed: ['Failed', 'danger'],
  interrupted: ['Interrupted', 'warning'],
  cancelled: ['Cancelled', 'muted'],
};
const jobState = (s: string) => state(...(JOB_STATUS[s] ?? [s, 'muted']));

const dateFmt = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' });
const when = (t: number | null | undefined) => (t ? dateFmt.format(new Date(t * 1000)) : '—');

function duration(secs: number): string {
  const s = Math.max(0, Math.floor(secs));
  const hh = Math.floor(s / 3600);
  const mm = Math.floor((s % 3600) / 60);
  const ss = s % 60;
  if (hh) return `${hh}h ${mm}m`;
  if (mm) return `${mm}m ${ss}s`;
  return `${ss}s`;
}

const jobLink = (id: string) => h('a', { href: `/jobs/${encodeURIComponent(id)}`, class: 'mono' }, id);

// ---------------------------------------------------------------- shell

function initials(email: string): string {
  const local = email.split('@')[0] ?? '';
  const parts = local.split(/[._+-]+/).filter(Boolean);
  const s = parts.length > 1 ? parts[0][0] + parts[1][0] : local.slice(0, 1);
  return (s || '?').toUpperCase();
}

async function logout(button: HTMLButtonElement) {
  button.disabled = true;
  const r = await send('POST', '/logout');
  if (r.ok || r.error.status === 401) {
    location.assign('/login');
    return;
  }
  button.disabled = false;
  setFormError(r.error.message);
}

function wireMenus() {
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

async function shell(): Promise<Me | null> {
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
function reauth(): Promise<boolean> {
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
async function withReauth<T>(action: () => Promise<Result<T>>): Promise<Result<T> | null> {
  const r = await action();
  if (r.ok || r.error.code !== 'reauth_required') return r;
  if (!(await reauth())) return null;
  return action();
}

// ---------------------------------------------------------------- balance

async function showBalance(): Promise<Balance | null> {
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

// ---------------------------------------------------------------- pages

function jobsTable(label: string, jobs: Job[]) {
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

async function overview() {
  const me = await shell();
  if (!me) return;
  void showBalance();

  const { status, compute_enabled } = me.account;
  const contact = h('a', { href: CONTACT }, 'contact us');
  $('access-state')!.hidden = false;
  if (status !== 'active') {
    $('access-dot')!.className = 'dot dot-danger';
    setText('access-word', 'Suspended');
    fill('access-text', "Your account is suspended: you can see your balance and jobs, but can't submit jobs or create keys. To sort it out, ", contact, '.');
  } else if (compute_enabled) {
    $('access-dot')!.className = 'dot dot-success';
    setText('access-word', 'Enabled');
    fill('access-text', 'Your account can run jobs. Submit them with an ', h('a', { href: '/keys' }, 'API key'), '.');
  } else {
    $('access-dot')!.className = 'dot dot-warning';
    setText('access-word', 'Awaiting enablement');
    fill('access-text', "We're turning compute on account by account while we start small, so your account can't run jobs yet. To ask for access, ", contact, '.');
  }

  const r = await get<List<Job>>('/jobs?limit=5');
  if (!r.ok) {
    fill('recent-jobs', muted(notBuilt(r.error) ? 'Your most recent jobs will appear here.' : r.error.message));
    return;
  }
  if (r.data.data.length === 0) {
    fill('recent-jobs', muted('No jobs yet. Create an ', h('a', { href: '/keys' }, 'API key'), ', then follow the ', h('a', { href: QUICK_START }, 'quick start'), ' to submit one.'));
    return;
  }
  fill('recent-jobs', jobsTable('Recent jobs', r.data.data));
}

async function keys() {
  const me = await shell();
  if (!me) return;
  const openButton = $<HTMLButtonElement>('create-open')!;
  const create = $('create')!;
  const form = $<HTMLFormElement>('create-form')!;
  const reveal = $('reveal')!;
  const status = $('reveal-status')!;

  const active = (k: Key) => !k.revoked_at && (!k.expires_at || k.expires_at * 1000 > Date.now());

  async function load() {
    const r = await get<Key[] | List<Key>>('/keys');
    if (!r.ok) {
      setText('keys-count', '—');
      fill('keys-list', muted(notBuilt(r.error) ? 'Your API keys will be listed here once keys are live in the dashboard.' : r.error.message));
      return;
    }
    const list = listOf(r.data).data;
    setText('keys-count', String(list.filter(active).length));
    if (list.length === 0) {
      fill('keys-list', muted('No keys yet. Create one to submit jobs.'));
      return;
    }
    fill(
      'keys-list',
      table(
        'API keys',
        [{ text: 'Name' }, { text: 'Key' }, { text: 'Access' }, { text: 'Created' }, { text: 'Last used' }, { text: 'Status' }, { text: '' }],
        list.map((k) => {
          const live = active(k);
          const st = k.revoked_at ? state('Revoked', 'muted') : live ? state('Active', 'success') : state('Expired', 'muted');
          const revoke = live
            ? h('button', { type: 'button', class: 'link', 'data-revoke': String(k.id), 'data-display': k.display, 'aria-label': `Revoke ${k.name}` }, 'Revoke')
            : '';
          return [k.name, mono(k.display), k.scopes.includes('jobs') ? 'Jobs' : 'Read only', when(k.created_at), when(k.last_used_at), st, revoke];
        }),
      ),
    );
  }

  const nameField = input('key-name')!;
  const closeCreate = () => {
    create.hidden = true;
    openButton.hidden = false;
  };
  openButton.addEventListener('click', () => {
    form.reset();
    reveal.hidden = true;
    create.hidden = false;
    // One primary per view: the form's "Create key" replaces this one.
    openButton.hidden = true;
    nameField.focus();
  });
  $('create-cancel')!.addEventListener('click', () => {
    closeCreate();
    openButton.focus();
  });

  onSubmit(form, async () => {
    const name = nameField.value.trim();
    if (!name) {
      setFieldError('key-name', 'Give the key a name.');
      return focusFirstInvalid(form);
    }
    const scope = form.querySelector<HTMLInputElement>('input[name=scope]:checked')?.value ?? 'jobs';
    busy(form, true);
    const r = await withReauth(() =>
      send<Key & { secret: string }>('POST', '/keys', { name, scopes: scope === 'jobs' ? ['jobs', 'read'] : ['read'] }),
    );
    if (!r) return;
    if (!r.ok) {
      if (r.error.param === 'name') {
        setFieldError('key-name', r.error.message);
        return nameField.focus();
      }
      return showApiError(r.error);
    }
    create.hidden = true;
    // The secret exists only in this element until "I've saved it".
    $('reveal-secret')!.textContent = r.data.secret;
    status.textContent = '';
    reveal.hidden = false;
    $('reveal-title')!.focus();
    void load();
  });

  $('reveal-copy')!.addEventListener('click', async () => {
    const secret = $('reveal-secret')!.textContent ?? '';
    try {
      await navigator.clipboard.writeText(secret);
      status.textContent = 'Copied to the clipboard.';
    } catch {
      // Clipboard access refused: select the key so a keyboard copy works.
      const range = document.createRange();
      range.selectNodeContents($('reveal-secret')!);
      const sel = getSelection();
      sel?.removeAllRanges();
      sel?.addRange(range);
      status.textContent = "Couldn't copy automatically. The key is selected: press Ctrl+C or ⌘C.";
    }
  });
  $('reveal-done')!.addEventListener('click', () => {
    $('reveal-secret')!.textContent = '';
    getSelection()?.removeAllRanges();
    reveal.hidden = true;
    openButton.hidden = false;
    openButton.focus();
  });

  // Revoke: confirm in a dialog; errors stay inside it.
  const dialog = $<HTMLDialogElement>('revoke-dialog')!;
  const revokeError = $('revoke-error')!;
  let target: { id: string; display: string; opener: HTMLElement } | null = null;
  $('keys-list')!.addEventListener('click', (e) => {
    const b = (e.target as Element).closest<HTMLButtonElement>('button[data-revoke]');
    if (!b) return;
    target = { id: b.dataset.revoke!, display: b.dataset.display ?? '', opener: b };
    setText('revoke-display', target.display);
    revokeError.hidden = true;
    dialog.showModal();
  });
  $<HTMLFormElement>('revoke-form')!.addEventListener('submit', async (e) => {
    if (e.submitter?.getAttribute('value') !== 'revoke' || !target) return;
    e.preventDefault();
    const confirm = $<HTMLButtonElement>('revoke-confirm')!;
    confirm.disabled = true;
    const r = await withReauth(() => send('DELETE', `/keys/${encodeURIComponent(target!.id)}`));
    confirm.disabled = false;
    if (!r) return;
    if (!r.ok && r.error.status !== 404) {
      revokeError.querySelector('p')!.textContent = r.error.message;
      revokeError.hidden = false;
      return;
    }
    dialog.close();
    await load();
    status.textContent = '';
    const announce = $('keys-list')!;
    announce.prepend(h('p', { class: 'status-line', role: 'status' }, `Key ${target.display} revoked.`));
    $('main')?.focus();
  });

  await load();
}

function chipsInit(onChange: (status: string) => void) {
  const chips = Array.from(document.querySelectorAll<HTMLButtonElement>('#filters .chip'));
  for (const c of chips) {
    c.addEventListener('click', () => {
      for (const o of chips) o.setAttribute('aria-pressed', String(o === c));
      onChange(c.dataset.status ?? '');
    });
  }
}

async function jobs() {
  const me = await shell();
  if (!me) return;
  const more = $<HTMLButtonElement>('jobs-more')!;
  let filter = '';
  let cursor: string | undefined;
  let shown: Job[] = [];

  const query = (after?: string) => {
    const q = new URLSearchParams({ limit: '20' });
    for (const s of filter.split(',').filter(Boolean)) q.append('status', s);
    if (after) q.set('after', after);
    return `/jobs?${q}`;
  };

  async function load(append: boolean) {
    more.disabled = true;
    const r = await get<List<Job>>(query(append ? cursor : undefined));
    more.disabled = false;
    if (!r.ok) {
      more.hidden = true;
      fill('jobs-list', muted(notBuilt(r.error) ? 'Your jobs will be listed here once jobs are live in the dashboard.' : r.error.message));
      return;
    }
    shown = append ? shown.concat(r.data.data) : r.data.data;
    cursor = r.data.has_more ? r.data.next : undefined;
    more.hidden = !cursor;
    fill('jobs-list', shown.length ? jobsTable('Jobs', shown) : muted(filter ? 'No jobs with this status.' : 'No jobs yet.'));
  }

  chipsInit((s) => {
    filter = s;
    void load(false);
  });
  more.addEventListener('click', () => void load(true));

  const running = await get<List<Job>>('/jobs?status=running&limit=100');
  setText('jobs-running', running.ok ? `${running.data.data.length}${running.data.has_more ? '+' : ''}` : '—');
  await load(false);
}

function facts(id: string, rows: [string, Child][]) {
  fill(id, ...rows.flatMap(([k, v]) => [h('dt', {}, k), h('dd', {}, v ?? '—')]));
}

function eventDetail(e: JobEvent): string {
  const d = e.data ?? {};
  const err = d.error as { code?: string; message?: string } | undefined;
  if (err) return [err.code, err.message].filter(Boolean).join(': ');
  const p = d.placement as { cloud?: string; region?: string; instance_type?: string } | undefined;
  if (p) return [p.cloud, p.region, p.instance_type].filter(Boolean).join(' · ');
  const keys = Object.keys(d);
  return keys.length ? JSON.stringify(d) : '';
}

async function job() {
  const me = await shell();
  if (!me) return;
  const id = decodeURIComponent(location.pathname.split('/')[2] ?? '');
  setText('job-id', id);
  setText('job-h1', `Job ${id}`);
  document.title = `${id || 'Job'} — Unify Compute`;
  const loading = $('job-loading')!;
  if (!id) {
    loading.hidden = true;
    setFormError('This address has no job id.', { href: '/jobs', text: 'See all jobs' });
    return;
  }
  const path = `/jobs/${encodeURIComponent(id)}`;
  const cancelButton = $<HTMLButtonElement>('job-cancel')!;

  function render(j: Job) {
    const u = j.usage;
    setText('job-settled', u?.settled_cost_usd ?? '—');
    fill(
      'job-sub',
      jobState(j.status),
      u ? ` · USD ${u.hold_usd} held · ceiling USD ${u.estimated_cost_usd}` : '',
    );
    cancelButton.hidden = TERMINAL.has(j.status) || me!.account.status !== 'active';
    facts('job-details', [
      ['Status', jobState(j.status)],
      ['SKU', mono(j.sku)],
      ['Created', when(j.created_at)],
      ['Started', when(j.started_at)],
      ['Finished', when(j.finished_at)],
      ['Placement', j.placement ? mono(`${j.placement.cloud} · ${j.placement.region} · ${j.placement.instance_type}`) : '—'],
      ['Image', j.spec?.image ? mono(j.spec.image) : '—'],
      ['Command', j.spec?.command?.length ? mono(j.spec.command.join(' ')) : '—'],
      ['Max runtime', j.spec?.max_runtime_seconds ? duration(j.spec.max_runtime_seconds) : '—'],
      ...(j.error ? ([['Error', h('span', {}, mono(j.error.code), ` ${j.error.message}`)]] as [string, Child][]) : []),
      ...Object.entries(j.metadata ?? {}).map(([k, v]) => [k, mono(v)] as [string, Child]),
    ]);
    facts('job-usage', [
      ['GPU time', mono(`${duration(u?.gpu_seconds ?? 0)} (${u?.gpu_seconds ?? 0} s)`)],
      ['Charged', mono(`USD ${u?.settled_cost_usd ?? '—'}`)],
      ['Held now', mono(`USD ${u?.hold_usd ?? '—'}`)],
      ['Ceiling', mono(`USD ${u?.estimated_cost_usd ?? '—'}`)],
    ]);
  }

  const r = await get<Job>(path);
  loading.hidden = true;
  if (!r.ok) {
    setFormError(
      r.error.status === 404 ? "We couldn't find that job in your account." : r.error.message,
      { href: '/jobs', text: 'See all jobs' },
    );
    return;
  }
  render(r.data);
  $('job')!.hidden = false;

  // Cancel: confirm in a dialog, then show the Job the API answers.
  const dialog = $<HTMLDialogElement>('cancel-dialog')!;
  const cancelError = $('cancel-error')!;
  cancelButton.addEventListener('click', () => {
    cancelError.hidden = true;
    dialog.showModal();
  });
  $<HTMLFormElement>('cancel-form')!.addEventListener('submit', async (e) => {
    if (e.submitter?.getAttribute('value') !== 'cancel-job') return;
    e.preventDefault();
    const confirm = $<HTMLButtonElement>('cancel-confirm')!;
    confirm.disabled = true;
    const c = await send<Job>('POST', `${path}/cancel`);
    confirm.disabled = false;
    if (!c.ok && c.error.code !== 'job_terminal') {
      cancelError.querySelector('p')!.textContent = c.error.message;
      cancelError.hidden = false;
      return;
    }
    dialog.close();
    const fresh = c.ok ? c : await get<Job>(path);
    if (fresh.ok) render(fresh.data);
    setText('job-status-line', c.ok ? 'Cancel requested. GPU time stopped being charged when it was accepted.' : 'This job had already ended.');
    $('main')?.focus();
  });

  const ev = await get<List<JobEvent>>(`${path}/events`);
  if (!ev.ok) fill('job-events', muted(notBuilt(ev.error) ? 'Events will appear here: queued, placed, running, and how the job ended.' : ev.error.message));
  else if (ev.data.data.length === 0) fill('job-events', muted('No events yet.'));
  else
    fill(
      'job-events',
      table('Events', [{ text: 'Time' }, { text: 'Event' }, { text: 'Detail' }], ev.data.data.map((e) => [when(e.created_at), mono(e.type), eventDetail(e)])),
    );

  async function logs() {
    const l = await getText(`${path}/logs`);
    if (!l.ok) fill('job-logs', muted(notBuilt(l.error) ? 'Logs will appear here while the job runs.' : l.error.message));
    else if (!l.data) fill('job-logs', muted("No output yet. Logs are read from the job's machine while it runs; a finished job's logs are not kept yet."));
    else fill('job-logs', h('pre', { class: 'code-block', tabindex: '0', role: 'region', 'aria-label': 'Job logs' }, l.data));
  }
  $('logs-refresh')!.addEventListener('click', () => void logs());
  await logs();

  // GET /jobs/{id}/artifacts, as the job API (built in A7d). Against an
  // older compute-api without it, this says where the files can be fetched.
  const a = await get<List<Artifact> & { complete?: boolean; truncated?: boolean }>(`${path}/artifacts`);
  if (!a.ok) {
    fill(
      'job-artifacts',
      notBuilt(a.error)
        ? muted('Download links are not in the dashboard yet. With an API key: ', h('code', { class: 'inline' }, `GET /compute/v1/jobs/${id}/artifacts`), '.')
        : muted(a.error.message),
    );
    return;
  }
  if (!a.data.data.length) {
    fill('job-artifacts', muted(a.data.complete === false ? "Files the job writes to its output directories are uploaded when its command exits." : 'This job wrote no files.'));
    return;
  }
  fill(
    'job-artifacts',
    table(
      'Artifacts',
      [{ text: 'File' }, { text: 'Size', num: true }, { text: 'SHA-256' }, { text: 'Link expires' }],
      a.data.data.map((f) => [
        // Presigned S3 URLs: a download the person starts, never a request
        // the page makes. Anything but https is not a link.
        f.url.startsWith('https://') ? h('a', { href: f.url, class: 'mono', rel: 'noopener noreferrer', download: true }, f.path) : mono(f.path),
        mono(f.size.toLocaleString()),
        h('span', { class: 'mono', title: f.sha256 }, f.sha256.slice(0, 12) + '…'),
        when(f.expires_at),
      ]),
    ),
    a.data.truncated ? muted('Only the first 10,000 files are listed.') : null,
  );
}

function monthLabel(m: string) {
  const [y, mo] = m.split('-').map(Number);
  return new Intl.DateTimeFormat(undefined, { month: 'long', year: 'numeric', timeZone: 'UTC' }).format(Date.UTC(y, mo - 1, 1));
}
function shiftMonth(m: string, by: number) {
  const [y, mo] = m.split('-').map(Number);
  const d = new Date(Date.UTC(y, mo - 1 + by, 1));
  return `${d.getUTCFullYear()}-${String(d.getUTCMonth() + 1).padStart(2, '0')}`;
}

const keyLabel = (k: KeyRef) => (k.display ? `${k.display} · ${k.name || 'deleted key'}` : `key #${k.id} (deleted)`);

// One bar per day of the month, heights relative to the busiest day. Ink on
// paper like the rest of the dashboard; amber stays for the focal number.
function dayChart(days: Usage['by_day'], total: string) {
  const values = days.map((d) => cents(d.usd) ?? 0);
  const max = Math.max(...values, 1);
  const bars = days.map((d, i) =>
    h('div', {
      class: values[i] ? 'chart-bar' : 'chart-bar empty',
      style: values[i] ? `height:${Math.max((values[i] / max) * 100, 2).toFixed(1)}%` : undefined,
      title: `${d.date}: USD ${d.usd}, ${duration(d.gpu_seconds)}`,
    }),
  );
  // About five labels along the axis, each a day of its month; the first
  // and last day always.
  const step = Math.max(1, Math.round((days.length - 1) / 4));
  const ticks = days.filter((_, i) => i % step === 0 || i === days.length - 1).map((d) => d.date);
  const label = (date: string) => (days.length <= 31 ? String(Number(date.slice(8))) : date.slice(5));
  return h(
    'div',
    { class: 'chart-wrap', role: 'img', 'aria-label': `GPU charges by day from ${days[0]?.date} to ${days[days.length - 1]?.date}: USD ${total} in total` },
    h('div', { class: 'chart' }, ...bars),
    h('div', { class: 'chart-axis', 'aria-hidden': 'true' }, ...ticks.map((d) => h('span', {}, label(d)))),
  );
}

const isoDate = (d: Date) => d.toISOString().slice(0, 10);

// The period the usage page shows: a calendar month, a trailing window, or
// two dates. Serialised the way the API takes it.
type Period = { kind: 'month'; month: string } | { kind: 'range'; from: string; to: string };
function periodQuery(p: Period): URLSearchParams {
  return p.kind === 'month' ? new URLSearchParams({ month: p.month }) : new URLSearchParams({ from: p.from, to: p.to });
}
function periodLabel(p: Period): string {
  return p.kind === 'month' ? monthLabel(p.month) : `${p.from} to ${p.to}`;
}

function fillSelect(id: string, options: { value: string; label: string }[]) {
  const sel = $<HTMLSelectElement>(id);
  if (!sel) return;
  const keep = sel.value;
  sel.replaceChildren(h('option', { value: '' }, 'All'), ...options.map((o) => h('option', { value: o.value }, o.label)));
  sel.value = options.some((o) => o.value === keep) ? keep : '';
}

async function usage() {
  const me = await shell();
  if (!me) return;
  const now = new Date();
  const current = `${now.getUTCFullYear()}-${String(now.getUTCMonth() + 1).padStart(2, '0')}`;
  let month = current;
  let period: Period = { kind: 'month', month };
  const prev = $<HTMLButtonElement>('month-prev')!;
  const next = $<HTMLButtonElement>('month-next')!;
  const rangeSel = $<HTMLSelectElement>('usage-range')!;
  const monthControls = $('month-controls')!;
  const customControls = $('custom-controls')!;
  const fromIn = $<HTMLInputElement>('usage-from')!;
  const toIn = $<HTMLInputElement>('usage-to')!;
  const apply = $<HTMLButtonElement>('usage-apply')!;
  const exportLink = $<HTMLAnchorElement>('usage-export')!;
  const skuSel = $<HTMLSelectElement>('usage-sku')!;
  const keySel = $<HTMLSelectElement>('usage-key')!;
  const more = $<HTMLButtonElement>('usage-jobs-more')!;
  let rows: JobUsage[] = [];
  let cursor: string | undefined;

  function jobsTableView() {
    return rows.length
      ? table(
          'Charged jobs',
          [{ text: 'Job' }, { text: 'Status' }, { text: 'SKU' }, { text: 'API key' }, { text: 'Last charged' }, { text: 'GPU time', num: true }, { text: 'USD', num: true }],
          rows.map((j) => [jobLink(j.job_id), jobState(j.status), mono(j.sku), mono(keyLabel(j.key)), when(j.settled_at), mono(duration(j.gpu_seconds)), mono(j.usd)]),
        )
      : muted('No job was charged in this month with these filters.');
  }

  async function loadJobs(append: boolean) {
    if (!append) {
      rows = [];
      cursor = undefined;
    }
    const q = periodQuery(period);
    q.set('limit', '50');
    if (skuSel.value) q.set('sku', skuSel.value);
    if (keySel.value) q.set('key', keySel.value);
    if (cursor) q.set('after', cursor);
    const r = await get<JobUsageList>(`/usage/jobs?${q}`);
    if (!r.ok) {
      fill('usage-jobs', muted(notBuilt(r.error) ? 'Charged jobs will be listed here.' : r.error.message));
      more.hidden = true;
      return;
    }
    rows = rows.concat(r.data.data);
    cursor = r.data.next;
    fill('usage-jobs', jobsTableView());
    more.hidden = !r.data.has_more;
  }

  async function load() {
    setText('usage-month-label', periodLabel(period));
    next.disabled = period.kind !== 'month' || month >= current;
    exportLink.href = `${API}/usage/export?${periodQuery(period)}`;
    const r = await get<Usage>(`/usage?${periodQuery(period)}`);
    if (!r.ok) {
      setText('usage-total', '—');
      fill(
        'usage-breakdown',
        muted(notBuilt(r.error) ? 'Monthly usage will appear here, totalled from the settled charges in your balance history.' : r.error.message),
      );
      return;
    }
    const u = r.data;
    setText('usage-total', u.total_usd);
    setText('usage-sub', `${u.jobs} job${u.jobs === 1 ? '' : 's'} charged · ${duration(u.gpu_seconds)} of GPU time.`);
    fill('usage-days', u.by_day?.length ? dayChart(u.by_day, u.gpu_usd) : muted('Nothing was charged in this month.'));
    fill(
      'usage-keys',
      u.by_key?.length
        ? table(
            'GPU time by API key',
            [{ text: 'API key' }, { text: 'Jobs', num: true }, { text: 'GPU time', num: true }, { text: 'USD', num: true }],
            u.by_key.map((k) => [mono(keyLabel(k.key)), mono(String(k.jobs)), mono(duration(k.gpu_seconds)), mono(k.usd)]),
          )
        : muted('No API key was charged in this month.'),
    );
    fillSelect('usage-sku', (u.by_sku ?? []).map((s) => ({ value: s.sku, label: s.sku })));
    fillSelect('usage-key', (u.by_key ?? []).map((k) => ({ value: String(k.key.id), label: keyLabel(k.key) })));
    void loadJobs(false);
    fill(
      'usage-breakdown',
      table('Usage by meter', [{ text: 'Meter' }, { text: 'USD', num: true }], [
        ['GPU time', mono(u.gpu_usd)],
        ['Artifact storage', mono(u.storage_usd)],
        ['Egress', mono(u.egress_usd)],
      ]),
      u.by_sku?.length
        ? table(
            'GPU time by SKU',
            [{ text: 'SKU' }, { text: 'GPU time', num: true }, { text: 'USD', num: true }],
            u.by_sku.map((s) => [mono(s.sku), mono(duration(s.gpu_seconds)), mono(s.usd)]),
          )
        : null,
    );
  }
  prev.addEventListener('click', () => {
    month = shiftMonth(month, -1);
    period = { kind: 'month', month };
    void load();
  });
  next.addEventListener('click', () => {
    month = shiftMonth(month, 1);
    period = { kind: 'month', month };
    void load();
  });
  function trailing(days: number): Period {
    const end = new Date();
    const start = new Date(end.getTime() - (days - 1) * 86_400_000);
    return { kind: 'range', from: isoDate(start), to: isoDate(end) };
  }
  rangeSel.addEventListener('change', () => {
    const v = rangeSel.value;
    monthControls.hidden = v !== 'month';
    customControls.hidden = v !== 'custom';
    if (v === 'month') period = { kind: 'month', month };
    else if (v === '7d') period = trailing(7);
    else if (v === '30d') period = trailing(30);
    else {
      if (!fromIn.value) fromIn.value = isoDate(new Date(Date.now() - 29 * 86_400_000));
      if (!toIn.value) toIn.value = isoDate(new Date());
      period = { kind: 'range', from: fromIn.value, to: toIn.value };
    }
    void load();
  });
  apply.addEventListener('click', () => {
    if (!fromIn.value || !toIn.value) return;
    if (toIn.value < fromIn.value) [fromIn.value, toIn.value] = [toIn.value, fromIn.value];
    period = { kind: 'range', from: fromIn.value, to: toIn.value };
    void load();
  });
  skuSel.addEventListener('change', () => void loadJobs(false));
  keySel.addEventListener('change', () => void loadJobs(false));
  more.addEventListener('click', () => void loadJobs(true));
  await load();
}

const KIND: Record<string, string> = {
  topup: 'Top-up',
  hold: 'Hold',
  extend: 'Hold extended',
  settle: 'Charge',
  release: 'Released',
  refund: 'Refund',
  dispute: 'Dispute',
  dispute_reversal: 'Dispute reversed',
  admin_credit: 'Credit',
  admin_debit: 'Debit',
  writeoff: 'Write-off',
};

function refCell(ref: Entry['ref']): Child {
  const id = typeof ref === 'string' ? ref : ref?.id;
  if (!id) return '—';
  return id.startsWith('job_') ? jobLink(id) : mono(id);
}

const TOPUP_STATUS: Record<string, [string, Tone]> = {
  open: ['Awaiting payment', 'muted'],
  credited: ['Added', 'success'],
  expired: ['Not completed', 'muted'],
  failed: ['Failed', 'danger'],
  refunded: ['Refunded', 'muted'],
  partially_refunded: ['Partly refunded', 'muted'],
  disputed: ['Disputed', 'warning'],
  lost: ['Dispute lost', 'danger'],
};

// Same rule as compute-api (internal/payments ParseAmount): dollars with at
// most two decimals.
function cents(usd: string): number | null {
  const m = /^(0|[1-9]\d{0,6})(?:\.(\d{1,2}))?$/.exec(usd.trim());
  return m ? Number(m[1]) * 100 + Number((m[2] ?? '').padEnd(2, '0')) : null;
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

async function billing() {
  const me = await shell();
  if (!me) return;
  const balance = showBalance();
  const more = $<HTMLButtonElement>('history-more')!;
  const kindSel = $<HTMLSelectElement>('history-kind');
  let rows: Entry[] = [];
  let cursor: string | undefined;

  async function load(append: boolean) {
    more.disabled = true;
    const q = new URLSearchParams({ limit: '25' });
    for (const k of (kindSel?.value ?? '').split(',').filter(Boolean)) q.append('kind', k);
    if (append && cursor) q.set('after', cursor);
    const r = await get<Entry[] | List<Entry>>(`/transactions?${q}`);
    more.disabled = false;
    if (!r.ok) {
      more.hidden = true;
      fill(
        'history',
        muted(notBuilt(r.error) ? 'Your balance history will appear here: every credit, hold, charge and release on your account.' : r.error.message),
      );
      return;
    }
    const list = listOf(r.data);
    rows = append ? rows.concat(list.data) : list.data;
    cursor = list.has_more ? list.next : undefined;
    more.hidden = !cursor;
    fill(
      'history',
      rows.length
        ? table(
            'Balance history',
            [{ text: 'Date' }, { text: 'Entry' }, { text: 'Amount (USD)', num: true }, { text: 'Available after', num: true }, { text: 'Reference' }],
            rows.map((e) => [when(e.created_at), KIND[e.kind] ?? e.kind, mono(e.amount_usd), mono(e.available_after_usd), refCell(e.ref)]),
          )
        : muted('Nothing yet.'),
    );
  }
  more.addEventListener('click', () => void load(true));
  kindSel?.addEventListener('change', () => void load(false));

  // --- Card top-ups (B1). The page never credits anything: it sends the
  // browser to Stripe's Checkout page and, on the way back, waits for the
  // webhook to have done it.
  const openButton = $<HTMLButtonElement>('topup-open')!;
  const contact = $('contact-primary')!;
  const card = $('topup')!;
  const off = $('topup-off')!;
  const form = $<HTMLFormElement>('topup-form')!;
  const amount = input('amount_usd')!;
  const status = $('topup-status')!;
  let min = 1000;
  let max = 10000;

  async function loadTopups(): Promise<TopupList | null> {
    const r = await get<TopupList>('/topups');
    if (!r.ok) return null;
    const list = r.data.data;
    $('topups-section')!.hidden = list.length === 0;
    fill(
      'topups',
      table(
        'Card top-ups',
        [{ text: 'Date' }, { text: 'Amount (USD)', num: true }, { text: 'Status' }, { text: 'Receipt' }],
        list.map((t) => [
          when(t.created_at),
          mono(t.amount_usd),
          state(...(TOPUP_STATUS[t.status] ?? [t.status, 'muted'])),
          t.receipt_url && t.receipt_url.startsWith('https://') ? h('a', { href: t.receipt_url, rel: 'noopener noreferrer', target: '_blank' }, 'View receipt') : '—',
        ]),
      ),
    );
    return r.data;
  }

  const closeForm = () => {
    card.hidden = true;
    openButton.hidden = false;
  };
  openButton.addEventListener('click', () => {
    form.reset();
    clearFieldError('amount_usd');
    card.hidden = false;
    // One primary per view: the form's own replaces this one.
    openButton.hidden = true;
    amount.focus();
  });
  $('topup-cancel')!.addEventListener('click', () => {
    closeForm();
    openButton.focus();
  });

  onSubmit(form, async () => {
    const c = cents(amount.value);
    if (c === null) {
      setFieldError('amount_usd', 'Enter an amount in US dollars, like 20 or 20.00.');
      return focusFirstInvalid(form);
    }
    if (c < min || c > max) {
      setFieldError('amount_usd', `Enter an amount from USD ${(min / 100).toFixed(2)} to ${(max / 100).toFixed(2)}.`);
      return focusFirstInvalid(form);
    }
    busy(form, true);
    const r = await send<{ id: string; checkout_url: string }>('POST', '/topups', { amount_usd: (c / 100).toFixed(2) });
    if (!r.ok) return showApiError(r.error);
    if (!r.data.checkout_url.startsWith('https://')) return setFormError(GENERIC);
    // A full navigation to Stripe's hosted page: no Stripe script runs here,
    // and the CSP stays as strict as on every other page.
    busy(form, true);
    location.assign(r.data.checkout_url);
  });

  // Back from Checkout: /billing?topup=tu_…&checkout=success|cancelled.
  async function returned(id: string, outcome: string | null) {
    history.replaceState(null, '', '/billing');
    if (outcome === 'cancelled') {
      status.textContent = 'Payment cancelled. Nothing was charged.';
      return;
    }
    status.textContent = 'Payment received by Stripe. Adding it to your balance…';
    for (let i = 0; i < 15; i++) {
      const r = await get<Topup>(`/topups/${encodeURIComponent(id)}`);
      if (r.ok && r.data.status !== 'open') {
        status.textContent =
          r.data.status === 'credited' ? `USD ${r.data.amount_usd} added to your balance.` : 'This payment did not go through. Nothing was charged.';
        void showBalance();
        void loadTopups();
        void load(false);
        return;
      }
      if (!r.ok && r.error.status === 404) break;
      await sleep(2000);
    }
    status.textContent = 'Your payment is still being confirmed. It will appear in your balance within a few minutes; refresh this page to check.';
  }

  const q = new URLSearchParams(location.search);
  const back = q.get('topup');
  const [list] = await Promise.all([loadTopups(), load(false)]);
  if (list?.topups_enabled) {
    const b = await balance;
    min = cents(b?.limits?.topup_min_usd ?? '') ?? min;
    max = cents(b?.limits?.topup_max_usd ?? '') ?? max;
    setText('amount_usd-hint', `USD ${(min / 100).toFixed(2)} to ${(max / 100).toFixed(2)} per top-up. Your balance can hold up to USD ${b?.limits?.balance_max_usd ?? '200.00'}.`);
    off.hidden = true;
    contact.hidden = true;
    openButton.hidden = false;
  }
  if (back && /^tu_[0-9A-Z]{26}$/.test(back)) await returned(back, q.get('checkout'));
}

async function account() {
  const me = await shell();
  if (!me) return;
  setText('me-email', me.user.email);
  fill(
    'me-sub',
    'Account ',
    mono(String(me.account.id)),
    ` · since ${new Intl.DateTimeFormat(undefined, { dateStyle: 'medium' }).format(new Date(me.user.created_at * 1000))}`,
    me.account.status !== 'active' ? ` · ${me.account.status}` : '',
  );
  const out = $<HTMLButtonElement>('logout-main')!;
  out.addEventListener('click', () => void logout(out));

  const form = $<HTMLFormElement>('form')!;
  const status = $('password-status')!;
  wireConfirm();
  onSubmit(form, async () => {
    status.textContent = '';
    const current = input('current_password')!.value;
    if (!current) setFieldError('current_password', 'Enter your current password.');
    const pwOk = checkNewPassword();
    const confirmOk = checkConfirm();
    if (!current || !pwOk || !confirmOk) return focusFirstInvalid(form);
    busy(form, true);
    const r = await send('POST', '/password', {
      current_password: current,
      password: input('password')!.value,
      password_confirm: input('password_confirm')!.value,
    });
    if (!r.ok) return showApiError(r.error);
    form.reset();
    status.textContent = 'Password changed. Every other session has been logged out.';
  });
}

export const dashboard: Record<string, () => Promise<void>> = { overview, keys, jobs, job, usage, billing, account };
