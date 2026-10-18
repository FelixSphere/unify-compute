import { t } from '../i18n';
import { $, setFormError } from '../lib';
import { type Child, duration, fill, get, getText, h, type Job, jobState, type List, mono, muted, notBuilt, send, setText, shell, table, usd, when } from '../shell';

type JobEvent = { id: string; type: string; created_at: number; data: Record<string, unknown> };
type Artifact = { path: string; size: number; sha256: string; url: string; expires_at: number };

const TERMINAL = new Set(['succeeded', 'failed', 'interrupted', 'cancelled']);

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

export async function job() {
  const me = await shell();
  if (!me) return;
  const id = decodeURIComponent(location.pathname.split('/')[2] ?? '');
  setText('job-id', id);
  setText('job-h1', t('job.job-id', { id }));
  document.title = `${id || 'Job'} — Unify Compute`;
  const loading = $('job-loading')!;
  if (!id) {
    loading.hidden = true;
    setFormError(t('job.this-address-has-no-job'), { href: '/jobs', text: t('job.see-all-jobs') });
    return;
  }
  const path = `/jobs/${encodeURIComponent(id)}`;
  const cancelButton = $<HTMLButtonElement>('job-cancel')!;

  function render(j: Job) {
    const u = j.usage;
    setText('job-settled', usd(u?.settled_cost_usd));
    fill(
      'job-sub',
      jobState(j.status),
      u ? ` · USD ${usd(u.hold_usd)} held · ceiling USD ${usd(u.estimated_cost_usd)}` : '',
    );
    cancelButton.hidden = TERMINAL.has(j.status) || me!.account.status !== 'active';
    facts('job-details', [
      [t('common.status'), jobState(j.status)],
      ['SKU', mono(j.sku)],
      [t('common.created'), when(j.created_at)],
      [t('job.started'), when(j.started_at)],
      [t('job.finished'), when(j.finished_at)],
      [t('job.placement'), j.placement ? mono(`${j.placement.cloud} · ${j.placement.region} · ${j.placement.instance_type}`) : '—'],
      [t('job.image'), j.spec?.image ? mono(j.spec.image) : '—'],
      [t('job.command'), j.spec?.command?.length ? mono(j.spec.command.join(' ')) : '—'],
      [t('job.max-runtime'), j.spec?.max_runtime_seconds ? duration(j.spec.max_runtime_seconds) : '—'],
      ...(j.error ? ([[t('job.error'), h('span', {}, mono(j.error.code), ` ${j.error.message}`)]] as [string, Child][]) : []),
      ...Object.entries(j.metadata ?? {}).map(([k, v]) => [k, mono(v)] as [string, Child]),
    ]);
    facts('job-usage', [
      [t('common.gpu-time'), mono(`${duration(u?.gpu_seconds ?? 0)} (${u?.gpu_seconds ?? 0} s)`)],
      [t('job.charged'), mono(`USD ${u?.settled_cost_usd ?? '—'}`)],
      [t('job.held-now'), mono(`USD ${u?.hold_usd ?? '—'}`)],
      [t('job.ceiling'), mono(`USD ${u?.estimated_cost_usd ?? '—'}`)],
    ]);
  }

  const r = await get<Job>(path);
  loading.hidden = true;
  if (!r.ok) {
    setFormError(
      r.error.status === 404 ? t('job.we-couldn-t-find-that') : r.error.message,
      { href: '/jobs', text: t('job.see-all-jobs') },
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
    setText('job-status-line', c.ok ? t('job.cancel-requested-gpu-time-stopped') : t('job.this-job-had-already-ended'));
    $('main')?.focus();
  });

  const ev = await get<List<JobEvent>>(`${path}/events`);
  if (!ev.ok) fill('job-events', muted(notBuilt(ev.error) ? t('job.events-will-appear-here-queued') : ev.error.message));
  else if (ev.data.data.length === 0) fill('job-events', muted(t('job.no-events-yet')));
  else
    fill(
      'job-events',
      table(t('job.events'), [{ text: t('job.time') }, { text: t('job.event') }, { text: t('job.detail') }], ev.data.data.map((e) => [when(e.created_at), mono(e.type), eventDetail(e)])),
    );

  async function logs() {
    const l = await getText(`${path}/logs`);
    if (!l.ok) fill('job-logs', muted(notBuilt(l.error) ? t('job.logs-will-appear-here-while') : l.error.message));
    else if (!l.data) fill('job-logs', muted(t('job.no-output-yet-logs-are')));
    else fill('job-logs', h('pre', { class: 'code-block', tabindex: '0', role: 'region', 'aria-label': t('job.job-logs') }, l.data));
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
        ? muted(t('job.download-links-are-not-in'), h('code', { class: 'inline' }, `GET /compute/v1/jobs/${id}/artifacts`), '.')
        : muted(a.error.message),
    );
    return;
  }
  if (!a.data.data.length) {
    fill('job-artifacts', muted(a.data.complete === false ? t('job.files-the-job-writes-to') : t('job.this-job-wrote-no-files')));
    return;
  }
  fill(
    'job-artifacts',
    table(
      t('job.artifacts'),
      [{ text: t('job.file') }, { text: t('job.size'), num: true }, { text: 'SHA-256' }, { text: t('job.link-expires') }],
      a.data.data.map((f) => [
        // Presigned S3 URLs: a download the person starts, never a request
        // the page makes. Anything but https is not a link.
        f.url.startsWith('https://') ? h('a', { href: f.url, class: 'mono', rel: 'noopener noreferrer', download: true }, f.path) : mono(f.path),
        mono(f.size.toLocaleString()),
        h('span', { class: 'mono', title: f.sha256 }, f.sha256.slice(0, 12) + '…'),
        when(f.expires_at),
      ]),
    ),
    a.data.truncated ? muted(t('job.only-the-first-10-000')) : null,
  );
}
