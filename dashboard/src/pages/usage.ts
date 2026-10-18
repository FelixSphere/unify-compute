import { t, tag } from '../i18n';
import { $, API } from '../lib';
import { duration, empty, fill, get, h, jobLink, jobState, money, mono, muted, notBuilt, setText, shell, table, usd, when } from '../shell';

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

function monthLabel(m: string) {
  const [y, mo] = m.split('-').map(Number);
  return new Intl.DateTimeFormat(tag(), { month: 'long', year: 'numeric', timeZone: 'UTC' }).format(Date.UTC(y, mo - 1, 1));
}
function shiftMonth(m: string, by: number) {
  const [y, mo] = m.split('-').map(Number);
  const d = new Date(Date.UTC(y, mo - 1 + by, 1));
  return `${d.getUTCFullYear()}-${String(d.getUTCMonth() + 1).padStart(2, '0')}`;
}

const keyLabel = (k: KeyRef) => (k.display ? `${k.display} · ${k.name || t('usage.deleted-key')}` : t('usage.key-id-deleted', { id: k.id }));

// One bar per day of the month, heights relative to the busiest day. Ink on
// paper like the rest of the dashboard; amber stays for the focal number.
function dayChart(days: Usage['by_day'], total: string) {
  const values = days.map((d) => Number(d.usd) || 0);
  const max = Math.max(...values, 1);
  const bars = days.map((d, i) =>
    h('div', {
      class: values[i] ? 'chart-bar' : 'chart-bar empty',
      style: values[i] ? `height:${Math.max((values[i] / max) * 100, 2).toFixed(1)}%` : undefined,
      title: `${d.date}: USD ${usd(d.usd)}, ${duration(d.gpu_seconds)}`,
    }),
  );
  // About five labels along the axis, each a day of its month; the first
  // and last day always.
  const step = Math.max(1, Math.round((days.length - 1) / 4));
  const ticks = days.filter((_, i) => i % step === 0 || i === days.length - 1).map((d) => d.date);
  const label = (date: string) => (days.length <= 31 ? String(Number(date.slice(8))) : date.slice(5));
  return h(
    'div',
    { class: 'chart-wrap', role: 'img', 'aria-label': t('usage.chart-label', { from: days[0]?.date ?? '', to: days[days.length - 1]?.date ?? '', total: usd(total) }) },
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
  sel.replaceChildren(h('option', { value: '' }, t('common.all')), ...options.map((o) => h('option', { value: o.value }, o.label)));
  sel.value = options.some((o) => o.value === keep) ? keep : '';
}

export async function usage() {
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
          t('usage.charged-jobs'),
          [{ text: 'Job' }, { text: t('common.status') }, { text: 'SKU' }, { text: t('common.api-key') }, { text: t('usage.last-charged') }, { text: t('common.gpu-time'), num: true }, { text: 'USD', num: true }],
          rows.map((j) => [jobLink(j.job_id), jobState(j.status), mono(j.sku), mono(keyLabel(j.key)), when(j.settled_at), mono(duration(j.gpu_seconds)), money(j.usd)]),
        )
      : empty(t('usage.no-charged-jobs'), t('usage.no-job-was-charged-in'));
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
      fill('usage-jobs', muted(notBuilt(r.error) ? t('usage.charged-jobs-will-be-listed') : r.error.message));
      more.hidden = true;
      return;
    }
    rows = rows.concat(r.data.data);
    cursor = r.data.next;
    fill('usage-jobs', jobsTableView());
    more.hidden = !r.data.has_more;
  }

  const emptyBlock = $('usage-empty')!;
  const detail = $('usage-detail')!;
  async function load() {
    setText('usage-month-label', periodLabel(period));
    setText('usage-empty-label', periodLabel(period));
    next.disabled = period.kind !== 'month' || month >= current;
    exportLink.href = `${API}/usage/export?${periodQuery(period)}`;
    const r = await get<Usage>(`/usage?${periodQuery(period)}`);
    if (!r.ok) {
      setText('usage-total', '—');
      fill(
        'usage-breakdown',
        muted(notBuilt(r.error) ? t('usage.monthly-usage-will-appear-here') : r.error.message),
      );
      return;
    }
    const u = r.data;
    setText('usage-total', usd(u.total_usd));
    setText('usage-sub', t('usage.from-to-utc', { from: u.from, to: u.to }));
    setText('usage-gpu-time', duration(u.gpu_seconds));
    setText('usage-gpu-sub', u.jobs === 1 ? t('usage.one-job-charged') : t('usage.jobs-charged', { n: u.jobs }));
    // A period with no charges says so once, instead of four empty sections.
    const nothing = u.jobs === 0 && Number(u.total_usd) === 0;
    emptyBlock.hidden = !nothing;
    detail.hidden = nothing;
    if (nothing) return;
    fill('usage-days', u.by_day?.length ? dayChart(u.by_day, u.gpu_usd) : muted(t('usage.nothing-was-charged-in-this')));
    fill(
      'usage-keys',
      u.by_key?.length
        ? table(
            t('usage.gpu-time-by-api-key'),
            [{ text: t('common.api-key') }, { text: t('nav.jobs'), num: true }, { text: t('common.gpu-time'), num: true }, { text: 'USD', num: true }],
            u.by_key.map((k) => [mono(keyLabel(k.key)), mono(String(k.jobs)), mono(duration(k.gpu_seconds)), money(k.usd)]),
          )
        : muted(t('usage.no-api-key-was-charged')),
    );
    fillSelect('usage-sku', (u.by_sku ?? []).map((s) => ({ value: s.sku, label: s.sku })));
    fillSelect('usage-key', (u.by_key ?? []).map((k) => ({ value: String(k.key.id), label: keyLabel(k.key) })));
    void loadJobs(false);
    fill(
      'usage-breakdown',
      table(t('usage.usage-by-meter'), [{ text: t('usage.meter') }, { text: 'USD', num: true }], [
        [t('common.gpu-time'), money(u.gpu_usd)],
        [t('usage.artifact-storage'), money(u.storage_usd)],
        [t('usage.egress'), money(u.egress_usd)],
      ]),
      u.by_sku?.length
        ? table(
            t('usage.gpu-time-by-sku'),
            [{ text: 'SKU' }, { text: t('common.gpu-time'), num: true }, { text: 'USD', num: true }],
            u.by_sku.map((s) => [mono(s.sku), mono(duration(s.gpu_seconds)), money(s.usd)]),
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
