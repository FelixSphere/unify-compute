import { $ } from '../lib';
import {
  type Balance,
  CONTACT,
  type Child,
  duration,
  fill,
  get,
  h,
  type Job,
  jobLink,
  jobsTable,
  jobState,
  type List,
  listOf,
  mono,
  muted,
  notBuilt,
  setText,
  shell,
  showBalance,
  when,
} from '../shell';

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
      h('p', { class: 'step-title' }, h('span', { class: 'sr-only' }, s.done ? 'Done: ' : 'To do: '), s.title),
      muted('', ...(s.done ? s.doneText : s.todoText)),
    ),
    s.done || s.actions.length === 0 ? null : h('div', { class: 'step-actions' }, ...s.actions),
  );
}

const positive = (usd: string | undefined) => Number(usd ?? 0) > 0;
const monthName = (ym: string | undefined) => {
  if (!ym) return 'this month';
  const [y, m] = ym.split('-').map(Number);
  return new Intl.DateTimeFormat(undefined, { month: 'long', year: 'numeric', timeZone: 'UTC' }).format(new Date(Date.UTC(y, m - 1, 1)));
};

function showAccess(status: string, computeEnabled: boolean) {
  const contact = h('a', { href: CONTACT }, 'contact us');
  $('access-state')!.hidden = false;
  if (status !== 'active') {
    $('access-dot')!.className = 'dot dot-danger';
    setText('access-word', 'Suspended');
    fill('access-text', "Your account is suspended: you can see your balance and jobs, but can't submit jobs or create keys. To sort it out, ", contact, '.');
  } else if (computeEnabled) {
    $('access-dot')!.className = 'dot dot-success';
    setText('access-word', 'Enabled');
    fill('access-text', 'Your account can run jobs. Submit them with an ', h('a', { href: '/keys' }, 'API key'), '.');
  } else {
    $('access-dot')!.className = 'dot dot-warning';
    setText('access-word', 'Awaiting enablement');
    fill('access-text', "We're turning compute on account by account while we start small, so your account can't run jobs yet. To ask for access, ", contact, '.');
  }
}

async function showSpend() {
  const r = await get<Usage>('/usage');
  if (!r.ok) {
    setText('spend-sub', notBuilt(r.error) ? "This month's spend will show here once usage is live in the dashboard." : r.error.message);
    return;
  }
  const u = r.data;
  setText('spend-total', u.total_usd);
  fill(
    'spend-sub',
    u.jobs === 0
      ? `Nothing charged in ${monthName(u.month)} yet. `
      : `Settled charges for ${u.jobs} ${u.jobs === 1 ? 'job' : 'jobs'} in ${monthName(u.month)} (UTC). `,
    h('a', { href: '/usage' }, 'Usage'),
  );
}

async function showRunning() {
  const r = await get<List<Job>>('/jobs?' + ACTIVE.map((s) => 'status=' + s).join('&') + '&limit=10');
  if (!r.ok) {
    fill('running', muted(notBuilt(r.error) ? 'A running job will show here.' : r.error.message));
    return;
  }
  const [j, ...rest] = r.data.data;
  if (!j) {
    fill('running', muted('Nothing is running. ', h('a', { href: '/jobs' }, 'All jobs')));
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
        ? h('div', {}, h('dt', {}, 'Running for'), h('dd', {}, mono(duration(now - j.started_at))))
        : h('div', {}, h('dt', {}, 'Submitted'), h('dd', {}, when(j.created_at))),
      est ? h('div', {}, h('dt', {}, 'Estimated so far'), h('dd', {}, mono('USD ' + est))) : null,
    ),
    rest.length || r.data.has_more ? muted('Other jobs are queued or running too. ', h('a', { href: '/jobs' }, 'All jobs')) : null,
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
      title: 'Verify your email',
      done: true,
      doneText: [`You confirmed ${email}.`],
      todoText: [],
      actions: [],
    },
    {
      title: 'Get compute enabled',
      done: computeEnabled,
      doneText: ['Your account can run jobs.'],
      todoText: [
        "UnifyAI turns compute on by hand, account by account, while we start small. Until then a job is refused, but you can add credit and create keys.",
      ],
      actions: [link(CONTACT, 'Ask for access')],
    },
    {
      title: 'Add credit',
      // A job needs a hold on the balance, so any job means credit was added.
      done: positive(b.available_usd) || positive(b.held_usd) || hasJob,
      doneText: ['Jobs are paid from your prepaid balance.'],
      todoText: ['Jobs are paid from a prepaid balance in USD. Top it up by card on Billing.'],
      actions: [link('/billing', 'Add credit')],
    },
    {
      title: 'Create an API key',
      done: listOf(k.data).data.length > 0,
      doneText: ['Your key submits jobs from your own code.'],
      todoText: ['A key lets your own code submit jobs to the job API. You see the secret once, when you create it.'],
      actions: [link('/keys', 'Create a key')],
    },
    {
      title: 'Run your first job',
      done: hasJob,
      doneText: ['Its status, logs and cost are under Jobs.'],
      todoText: ['Run a template from the Marketplace, or submit your own job with the quick start.'],
      actions: [link('/marketplace', 'Marketplace'), link(QUICK_START, 'Quick start')],
    },
  ];
  const done = steps.filter((s) => s.done).length;
  if (done === steps.length) return;
  setText('onboarding-progress', `${done} of ${steps.length} done`);
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
    fill('recent-jobs', muted(notBuilt(r.error) ? 'Your most recent jobs will appear here.' : r.error.message));
    return;
  }
  if (r.data.data.length === 0) {
    fill('recent-jobs', muted('No jobs yet. Run a template from the ', h('a', { href: '/marketplace' }, 'Marketplace'), ', or create an ', h('a', { href: '/keys' }, 'API key'), ' and follow the ', h('a', { href: QUICK_START }, 'quick start'), '.'));
    return;
  }
  fill('recent-jobs', jobsTable('Recent jobs', r.data.data));
}
