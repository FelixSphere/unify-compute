import { t } from '../i18n';
import { $ } from '../lib';
import { empty, fill, get, h, type Job, jobsTable, type List, muted, notBuilt, rich, setText, shell } from '../shell';

function chipsInit(onChange: (status: string) => void) {
  const chips = Array.from(document.querySelectorAll<HTMLButtonElement>('#filters .chip'));
  for (const c of chips) {
    c.addEventListener('click', () => {
      for (const o of chips) o.setAttribute('aria-pressed', String(o === c));
      onChange(c.dataset.status ?? '');
    });
  }
}

export async function jobs() {
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
      fill('jobs-list', muted(notBuilt(r.error) ? t('jobs.your-jobs-will-be-listed') : r.error.message));
      return;
    }
    shown = append ? shown.concat(r.data.data) : r.data.data;
    cursor = r.data.has_more ? r.data.next : undefined;
    more.hidden = !cursor;
    fill(
      'jobs-list',
      shown.length
        ? jobsTable(t('nav.jobs'), shown)
        : filter
          ? empty(t('jobs.no-jobs-with-this-status'))
          : empty(t('jobs.no-jobs-yet'), rich('jobs.no-jobs-yet-text')),
    );
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
