import { $, setFormError } from '../lib';
import { type Child, duration, fill, get, getText, h, type Job, jobState, type List, mono, muted, notBuilt, send, setText, shell, table, when } from '../shell';

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
