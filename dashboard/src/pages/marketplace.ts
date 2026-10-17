import { t as tr } from '../i18n';
import { $, busy, clearErrors, focusFirstInvalid, input, onSubmit, setFieldError, setFormError, showApiError } from '../lib';
import { CONTACT, fill, get, h, type Job, type List, money, mono, muted, notBuilt, send, setText, shell, state, table, type Template, usd } from '../shell';

type SKU = {
  id: string;
  gpu?: string;
  accelerator?: string;
  gpu_count?: number;
  gpu_memory_gb?: number;
  vcpus?: number;
  memory_gb?: number;
  tier?: string;
  interruptible?: boolean;
  price_usd_per_hour: string;
  available: boolean;
  providers?: { cloud: string }[];
};

// The catalogue as a table: what can be chosen now, and what is coming.
// Available first, then by price. Signed-in pages may show the hourly rate
// (operator, 2026-10-14); the public marketplace page shows none.
function gpuTable(catalogue: SKU[]): HTMLElement {
  const rows = [...catalogue].sort((a, b) => Number(b.available) - Number(a.available) || Number(a.price_usd_per_hour) - Number(b.price_usd_per_hour));
  return table(
    tr('marketplace.gpus'),
    [{ text: tr('marketplace.col-gpu') }, { text: tr('marketplace.col-memory'), num: true }, { text: tr('marketplace.col-cpu') }, { text: tr('marketplace.col-tier') }, { text: tr('marketplace.col-billing') }, { text: tr('marketplace.col-rate'), num: true }, { text: tr('common.status') }],
    rows.map((k) => [
      h('span', { class: 'cell-stack' }, h('strong', {}, `${k.gpu_count && k.gpu_count > 1 ? `${k.gpu_count} × ` : ''}${k.gpu ?? k.id}`), mono(k.id)),
      mono(k.gpu_memory_gb ? `${k.gpu_memory_gb} GB` : '—'),
      mono(k.vcpus && k.memory_gb ? `${k.vcpus} · ${k.memory_gb} GB` : '—'),
      k.tier ? tr(`marketplace.tier-${k.tier}`) : '—',
      k.interruptible ? tr('marketplace.spot') : tr('marketplace.on-demand'),
      money(k.price_usd_per_hour),
      k.available ? state(tr('marketplace.available'), 'success') : state(tr('marketplace.coming'), 'muted'),
    ]),
  );
}

const PUBLISHER = 'Unify Compute';
const summary = (text: string) => (text.length > 160 ? text.slice(0, 157).trimEnd() + '…' : text);
const runtimeLabel = (secs: number) => (secs % 3600 === 0 ? `${secs / 3600} h` : secs % 60 === 0 ? `${secs / 60} min` : `${secs} s`);

// ledger.InitialHoldSeconds: a submit holds at most this much GPU time up
// front, and the hold is extended while the job runs, up to max runtime.
const INITIAL_HOLD_SECONDS = 7200;
// sku.QuotaPerUSD: holds are whole quota units, rounded half up.
const QUOTA_PER_USD = 500000;

// The hold the ledger places for `seconds` at `rate` (USD per hour, as the
// SKU route returns it), in USD with the job API's four decimals; two when
// the last two are zero. Exact: the same rounding as sku.EstimateQuota and
// the job API's quotaUSD, so it matches a 402's figures.
function holdUSD(rate: string, seconds: number): string | null {
  const m = /^(\d+)(?:\.(\d+))?$/.exec(rate);
  if (!m || !Number.isInteger(seconds) || seconds < 0) return null;
  const frac = m[2] ?? '';
  const num = BigInt(m[1] + frac) * BigInt(seconds) * BigInt(QUOTA_PER_USD);
  const den = BigInt(10) ** BigInt(frac.length) * BigInt(3600);
  const two = BigInt(2);
  const quota = (two * num + den) / (two * den);
  const tenThousandths = (two * quota * BigInt(10000) + BigInt(QUOTA_PER_USD)) / (two * BigInt(QUOTA_PER_USD));
  const whole = tenThousandths / BigInt(10000);
  const rest = String(tenThousandths % BigInt(10000)).padStart(4, '0');
  return `${whole}.${rest.endsWith('00') ? rest.slice(0, 2) : rest}`;
}

function field(id: string, label: string, control: HTMLElement, hint?: string): HTMLElement {
  control.id = id;
  control.setAttribute('aria-describedby', `${id}-hint ${id}-error`);
  return h(
    'div',
    { class: 'field' },
    h('label', { for: id }, label),
    control,
    hint ? h('p', { class: 'hint', id: `${id}-hint` }, hint) : null,
    h('p', { class: 'field-error', id: `${id}-error`, hidden: true }),
  );
}

function tile(t: Template): HTMLElement {
  return h(
    'li',
    { class: 'tile' },
    h('div', { class: 'tile-top' }, h('code', {}, t.id), h('span', { class: t.available ? 'badge' : 'badge muted' }, t.available ? tr('marketplace.available') : tr('marketplace.not-yet'))),
    h('h3', {}, t.title),
    muted(summary(t.description)),
    h(
      'dl',
      { class: 'facts' },
      h('div', {}, h('dt', {}, 'GPU'), h('dd', { class: 'mono' }, t.sku)),
      h('div', {}, h('dt', {}, tr('marketplace.default-runtime')), h('dd', {}, runtimeLabel(t.max_runtime_seconds))),
      h('div', {}, h('dt', {}, tr('marketplace.inputs')), h('dd', {}, t.inputs.length ? t.inputs.map((m) => m.mount).join(', ') : 'none')),
      h('div', {}, h('dt', {}, tr('marketplace.publisher')), h('dd', {}, t.publisher ?? PUBLISHER)),
    ),
    h('button', { type: 'button', class: 'secondary', 'data-run': t.id, disabled: !t.available, 'aria-label': tr('marketplace.run-title', { title: t.title }) }, t.available ? tr('marketplace.run-this') : t.unavailable_reason ?? tr('marketplace.not-runnable-yet')),
  );
}

export async function marketplace() {
  const me = await shell();
  if (!me) return;
  $('access-note')!.hidden = me.account.status === 'active' && me.account.compute_enabled;

  const skus = await get<List<SKU>>('/skus');
  const catalogue = skus.ok ? skus.data.data : [];
  fill('gpu-list', skus.ok ? (catalogue.length ? gpuTable(catalogue) : muted(tr('marketplace.no-gpus'))) : muted(notBuilt(skus.error) ? tr('marketplace.gpus-will-appear') : skus.error.message));

  const r = await get<List<Template>>('/templates');
  if (!r.ok) {
    fill('tpl-list', muted(notBuilt(r.error) ? tr('marketplace.templates-will-appear-here-once') : r.error.message));
    return;
  }
  const templates = r.data.data;
  setText('tpl-count', String(templates.length));
  if (templates.length === 0) {
    fill('tpl-list', muted(tr('marketplace.no-templates-are-offered-yet')));
    return;
  }
  const grid = h('ul', { class: 'tiles' }, ...templates.map(tile));
  fill('tpl-list', grid);

  const card = $('run')!;
  const form = $<HTMLFormElement>('run-form')!;
  const fields = $('run-fields')!;
  let opener: HTMLElement | null = null;
  const close = () => {
    card.hidden = true;
    fields.replaceChildren();
    opener?.focus();
  };
  $('run-cancel')!.addEventListener('click', close);

  let current: Template | null = null;
  // Signed-in pages may show the hourly rate (operator, 2026-10-14); the
  // public pages still show none (ADR-0012).
  const estimateEl = $('run-estimate')!;
  function estimate() {
    const t = current;
    if (!t) return;
    const id = $<HTMLSelectElement>('run-sku')?.value ?? t.sku;
    const rate = catalogue.find((k) => k.id === id)?.price_usd_per_hour;
    const runtimeField = input('run-runtime');
    const secs = runtimeField ? Number(runtimeField.value) : t.max_runtime_seconds;
    if (!rate) {
      estimateEl.hidden = true;
      return;
    }
    const hourly = holdUSD(rate, 3600);
    if (!Number.isInteger(secs) || secs < 1) {
      estimateEl.textContent = tr('marketplace.estimate-no-runtime', { sku: id, hourly: hourly ?? '' });
      estimateEl.hidden = false;
      return;
    }
    const held = holdUSD(rate, Math.min(secs, INITIAL_HOLD_SECONDS));
    const most = holdUSD(rate, secs);
    estimateEl.textContent =
      secs <= INITIAL_HOLD_SECONDS
        ? tr('marketplace.estimate-within-hold', { sku: id, hourly: hourly ?? '', held: held ?? '' })
        : tr('marketplace.estimate-beyond-hold', { sku: id, hourly: hourly ?? '', held: held ?? '', first: runtimeLabel(INITIAL_HOLD_SECONDS), max: runtimeLabel(secs), most: most ?? '' });
    estimateEl.hidden = false;
  }

  function open(t: Template, from: HTMLElement) {
    current = t;
    opener = from;
    const can = (f: string) => t.overridable.includes(f);
    setText('run-title', tr('marketplace.run-title', { title: t.title }));
    setText('run-sub', `${t.id} · ${t.description}`);
    const nodes: HTMLElement[] = [];
    t.inputs.forEach((m, i) => {
      nodes.push(
        field(`in-${i}`, tr('marketplace.input-for', { mount: m.mount }), h('input', { type: 'url', inputmode: 'url', autocomplete: 'off', placeholder: 'https://…/file', required: true }), `${m.description} ${tr('marketplace.input-hint', { mount: m.mount })}`),
      );
    });
    if (can('sku')) {
      const listed = catalogue.some((k) => k.id === t.sku) ? catalogue : [{ id: t.sku, price_usd_per_hour: '', available: true }, ...catalogue];
      const options = [...listed].sort((a, b) => Number(b.available) - Number(a.available) || Number(a.price_usd_per_hour) - Number(b.price_usd_per_hour));
      const label = (k: SKU) => {
        const name = k.gpu ? `${k.gpu}${k.interruptible ? ` (${tr('marketplace.spot')})` : ''} · ${k.id}` : k.id;
        const rate = k.price_usd_per_hour ? ` · USD ${usd(k.price_usd_per_hour)}/h` : '';
        return k.available ? name + rate : `${name} · ${tr('marketplace.coming')}`;
      };
      const select = h('select', {}, ...options.map((k) => h('option', { value: k.id, selected: k.id === t.sku, disabled: !k.available }, label(k))));
      nodes.push(field('run-sku', tr('marketplace.gpu-sku'), select, tr('marketplace.runs-on-unless', { sku: t.sku })));
      // No provider choice: which cloud runs a SKU is ours (ADR-0006, ADR-0013).
    }
    if (can('spec.env')) {
      for (const [k, v] of Object.entries(t.env)) {
        nodes.push(field(`env-${k}`, tr('marketplace.environment-var', { name: k }), h('input', { type: 'text', autocomplete: 'off', value: v }), undefined));
        (nodes[nodes.length - 1].querySelector('input') as HTMLInputElement).value = v;
      }
    }
    if (can('spec.max_runtime_seconds')) {
      const n = h('input', { type: 'number', inputmode: 'numeric', min: '1', step: '1', autocomplete: 'off' });
      n.value = String(t.max_runtime_seconds);
      nodes.push(field('run-runtime', tr('marketplace.maximum-runtime-seconds'), n, tr('marketplace.the-job-is-stopped-at')));
    }
    if (can('spec.command')) {
      const ta = h('textarea', { rows: '4', spellcheck: 'false' });
      ta.value = t.command.join('\n');
      nodes.push(field('run-command', tr('job.command'), ta, tr('marketplace.one-argument-per-line-as')));
    }
    fields.replaceChildren(...nodes);
    $('run-sku')?.addEventListener('change', estimate);
    input('run-runtime')?.addEventListener('input', estimate);
    estimate();
    clearErrors(form);
    card.hidden = false;
    $('run-title')!.focus();
    form.querySelector<HTMLInputElement>('input, select, textarea')?.focus();
  }

  grid.addEventListener('click', (e) => {
    const b = (e.target as Element).closest<HTMLButtonElement>('button[data-run]');
    const t = b && templates.find((x) => x.id === b.dataset.run);
    if (t) open(t, b);
  });

  onSubmit(form, async () => {
    const t = current;
    if (!t) return;
    const spec: Record<string, unknown> = {};
    const body: Record<string, unknown> = { template: t.id, spec };
    const inputs: { source: string; mount: string }[] = [];
    let bad = false;
    t.inputs.forEach((m, i) => {
      const v = input(`in-${i}`)!.value.trim();
      if (!/^https:\/\/\S+\/[^/?#]+$/.test(v)) {
        setFieldError(`in-${i}`, tr('marketplace.enter-an-https-url-that'));
        bad = true;
      }
      inputs.push({ source: v, mount: m.mount });
    });
    if (bad) return focusFirstInvalid(form);
    if (inputs.length) spec.inputs = inputs;
    const sku = $<HTMLSelectElement>('run-sku');
    if (sku && sku.value !== t.sku) body.sku = sku.value;
    const providers = Array.from(form.querySelectorAll<HTMLInputElement>('input[name=provider]:checked')).map((c) => c.value);
    if (providers.length) body.providers = providers;
    if (t.overridable.includes('spec.env')) {
      const env: Record<string, string> = {};
      let changed = false;
      for (const [k, v] of Object.entries(t.env)) {
        env[k] = input(`env-${k}`)?.value ?? v;
        if (env[k] !== v) changed = true;
      }
      if (changed) spec.env = env;
    }
    const runtime = input('run-runtime');
    if (runtime) {
      const n = Number(runtime.value);
      if (!Number.isInteger(n) || n < 1) {
        setFieldError('run-runtime', tr('marketplace.whole-seconds-at-least-1'));
        return runtime.focus();
      }
      if (n !== t.max_runtime_seconds) spec.max_runtime_seconds = n;
    }
    const command = $<HTMLTextAreaElement>('run-command');
    if (command) {
      const argv = command.value.split('\n').map((l) => l.trim()).filter(Boolean);
      if (argv.join('\n') !== t.command.join('\n')) spec.command = argv;
    }
    busy(form, true);
    const r = await send<Job>('POST', '/jobs', body);
    if (!r.ok) {
      const e = r.error;
      const target: Record<string, string> = { 'spec.inputs': 'in-0', sku: 'run-sku', 'spec.env': `env-${Object.keys(t.env)[0] ?? ''}`, 'spec.max_runtime_seconds': 'run-runtime', 'spec.command': 'run-command' };
      const id = e.param && target[e.param];
      if (id && setFieldError(id, e.message)) return input(id)?.focus();
      if (e.code === 'insufficient_balance') return setFormError(e.message, { href: '/billing', text: tr('marketplace.top-up') });
      if (e.code === 'compute_not_enabled') return setFormError(e.message, { href: CONTACT, text: tr('common.contact-us') });
      return showApiError(e);
    }
    location.assign(`/jobs/${encodeURIComponent(r.data.id)}`);
    await new Promise(() => {});
  });
}
