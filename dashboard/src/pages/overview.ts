import { $ } from '../lib';
import { CONTACT, fill, get, h, type Job, jobsTable, type List, muted, notBuilt, setText, shell, showBalance } from '../shell';

const QUICK_START = 'https://compute.unifyai.us/start';

export async function overview() {
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
