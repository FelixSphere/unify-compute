import { t, tag } from '../i18n';
import { $ } from '../lib';
import { type Balance, type Child, CONTACT, duration, empty, fill, get, h, type Job, jobLink, jobsTable, jobState, type List, listOf, mono, muted, notBuilt, rich, setText, shell, showBalance, usd, when } from '../shell';

const QUICK_START = 'https://compute.unifyai.us/start';
const ACTIVE = ['queued', 'provisioning', 'running'];

type Usage = { month?: string; total_usd: string; jobs: number };
type Key = { id: number };

// lucide Check (ISC). A constant string, so innerHTML carries nothing from the API.
const CHECK =
  '<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><path d="M20 6 9 17l-5-5"/></svg>';

type Step = { title: string; done: boolean; doneText: Child[]; todoText: Child[]; actions: HTMLAnchorElement[] };

function stepItem(s: Step, n: number): HTMLElement {
  const mark = h('span', { class: 'step-mark', 'aria-hidden': 'true' });
  if (s.done) mark.innerHTML = CHECK;
  else mark.textContent = String(n);
  return h(
    'li',
    { class: s.done ? 'step done' : 'step' },
    mark,
    h(
      'div',
      { class: 'step-body' },
      h('p', { class: 'step-title' }, h('span', { class: 'sr-only' }, s.done ? t('overview.done') : t('overview.to-do')), s.title),
      muted('', ...(s.done ? s.doneText : s.todoText)),
    ),
    s.done || s.actions.length === 0 ? null : h('div', { class: 'step-actions' }, ...s.actions),
  );
}

const positive = (usd: string | undefined) => Number(usd ?? 0) > 0;
const monthName = (ym: string | undefined) => {
  if (!ym) return 'this month';
  const [y, m] = ym.split('-').map(Number);
  return new Intl.DateTimeFormat(tag(), { month: 'long', year: 'numeric', timeZone: 'UTC' }).format(new Date(Date.UTC(y, m - 1, 1)));
};

function showAccess(status: string, computeEnabled: boolean) {
  const contactLink = `<a href="${CONTACT}">${t('common.contact-us-lower')}</a>`;
  $('access-state')!.hidden = false;
  if (status !== 'active') {
    $('access-dot')!.className = 'dot dot-danger';
    setText('access-word', t('overview.suspended'));
    fill('access-text', rich('overview.access-suspended', { contact: contactLink }));
  } else if (computeEnabled) {
    $('access-dot')!.className = 'dot dot-success';
    setText('access-word', t('overview.enabled'));
    fill('access-text', rich('overview.access-enabled'));
  } else {
    $('access-dot')!.className = 'dot dot-warning';
    setText('access-word', t('overview.awaiting-enablement'));
    fill('access-text', rich('overview.access-awaiting', { contact: contactLink }));
  }
}

async function showSpend() {
  const r = await get<Usage>('/usage');
  if (!r.ok) {
    setText('spend-total', '—');
    setText('spend-sub', notBuilt(r.error) ? t('overview.this-month-s-spend-will') : r.error.message);
    return;
  }
  const u = r.data;
  setText('spend-total', usd(u.total_usd));
  fill(
    'spend-sub',
    u.jobs === 0
      ? t('overview.nothing-charged-yet', { month: monthName(u.month) })
      : u.jobs === 1
        ? t('overview.settled-one-job', { month: monthName(u.month) })
        : t('overview.settled-jobs', { n: u.jobs, month: monthName(u.month) }),
    ' ',
    h('a', { href: '/usage' }, t('nav.usage')),
  );
}

async function showRunning() {
  const r = await get<List<Job>>('/jobs?' + ACTIVE.map((s) => 'status=' + s).join('&') + '&limit=10');
  if (!r.ok) {
    fill('running', muted(notBuilt(r.error) ? t('overview.a-running-job-will-show') : r.error.message));
    return;
  }
  const [j, ...rest] = r.data.data;
  if (!j) {
    fill('running', h('p', { class: 'stat-value' }, '0'), muted(t('overview.nothing-is-running'), ' ', h('a', { href: '/jobs' }, t('overview.all-jobs'))));
    return;
  }
  const now = Date.now() / 1000;
  const est = j.usage?.estimated_cost_usd;
  fill(
    'running',
    h('p', { class: 'running-head' }, jobLink(j.id), jobState(j.status)),
    h(
      'dl',
      { class: 'facts' },
      h('div', {}, h('dt', {}, 'GPU'), h('dd', {}, mono(j.sku))),
      j.started_at
        ? h('div', {}, h('dt', {}, t('overview.running-for')), h('dd', {}, mono(duration(now - j.started_at))))
        : h('div', {}, h('dt', {}, t('overview.submitted')), h('dd', {}, when(j.created_at))),
      est ? h('div', {}, h('dt', {}, t('overview.estimated-so-far')), h('dd', {}, mono('USD ' + usd(est)))) : null,
    ),
    rest.length || r.data.has_more ? muted(t('overview.other-jobs-too'), ' ', h('a', { href: '/jobs' }, t('overview.all-jobs'))) : null,
  );
}

async function showOnboarding(email: string, computeEnabled: boolean, balance: Promise<Balance | null>, jobs: Promise<Job[] | null>) {
  const [b, k, js] = await Promise.all([balance, get<Key[] | List<Key>>('/keys'), jobs]);
  // Without all three the checklist would guess; the cards below still say what they can.
  if (!b || !k.ok || !js) return;
  const hasJob = js.length > 0;
  const link = (href: string, text: string) => h('a', { href }, text);
  const steps: Step[] = [
    {
      title: t('overview.verify-your-email'),
      done: true,
      doneText: [t('overview.you-confirmed', { email })],
      todoText: [],
      actions: [],
    },
    {
      title: t('overview.get-compute-enabled'),
      done: computeEnabled,
      doneText: [t('overview.your-account-can-run-jobs')],
      todoText: [
        t('overview.unifyai-turns-compute-on-by'),
      ],
      actions: [link(CONTACT, t('overview.ask-for-access'))],
    },
    {
      title: t('billing.add-credit'),
      // A job needs a hold on the balance, so any job means credit was added.
      done: positive(b.available_usd) || positive(b.held_usd) || hasJob,
      doneText: [t('overview.jobs-are-paid-from-your')],
      todoText: [t('overview.jobs-are-paid-from-a')],
      actions: [link('/billing', t('billing.add-credit'))],
    },
    {
      title: t('keys.create-an-api-key'),
      done: listOf(k.data).data.length > 0,
      doneText: [t('overview.your-key-submits-jobs-from')],
      todoText: [t('overview.a-key-lets-your-own')],
      actions: [link('/keys', t('overview.create-a-key'))],
    },
    {
      title: t('overview.run-your-first-job'),
      done: hasJob,
      doneText: [t('overview.its-status-logs-and-cost')],
      todoText: [t('overview.run-a-template-from-the')],
      actions: [link('/marketplace', t('nav.marketplace')), link(QUICK_START, t('overview.quick-start'))],
    },
  ];
  const done = steps.filter((s) => s.done).length;
  if (done === steps.length) return;
  setText('onboarding-progress', t('overview.n-of-m-done', { done, total: steps.length }));
  fill('onboarding-steps', ...steps.map((s, i) => stepItem(s, i + 1)));
  $('onboarding')!.hidden = false;
  // The checklist's second step says the same as the access card.
  $('access')!.hidden = true;
}

export async function overview() {
  const me = await shell();
  if (!me) return;
  const { status, compute_enabled } = me.account;
  showAccess(status, compute_enabled);
  const balance = showBalance();
  void showSpend();
  void showRunning();

  const recent = get<List<Job>>('/jobs?limit=5');
  const jobs = recent.then((r) => (r.ok ? r.data.data : null));
  if (status === 'active') void showOnboarding(me.user.email, compute_enabled, balance, jobs);

  const r = await recent;
  if (!r.ok) {
    fill('recent-jobs', muted(notBuilt(r.error) ? t('overview.your-most-recent-jobs-will') : r.error.message));
    return;
  }
  if (r.data.data.length === 0) {
    fill('recent-jobs', empty(t('jobs.no-jobs-yet'), rich('overview.no-jobs-yet-text', { quickstart: QUICK_START })));
    return;
  }
  fill('recent-jobs', jobsTable(t('overview.recent-jobs'), r.data.data));
}
