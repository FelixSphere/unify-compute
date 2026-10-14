import { $, busy, clearErrors, focusFirstInvalid, input, onSubmit, setFieldError, setFormError, showApiError } from '../lib';
import { CONTACT, fill, get, h, type Job, type List, muted, notBuilt, send, setText, shell, type Template } from '../shell';

type SKU = { id: string; gpu?: string; accelerator?: string; gpu_count?: number; price_usd_per_hour: string; available: boolean; providers?: { cloud: string }[] };

const PUBLISHER = 'Unify Compute';
const summary = (text: string) => (text.length > 160 ? text.slice(0, 157).trimEnd() + '…' : text);
const runtimeLabel = (secs: number) => (secs % 3600 === 0 ? `${secs / 3600} h` : secs % 60 === 0 ? `${secs / 60} min` : `${secs} s`);

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
    h('div', { class: 'tile-top' }, h('code', {}, t.id), h('span', { class: t.available ? 'badge' : 'badge muted' }, t.available ? 'Available' : 'Not yet')),
    h('h3', {}, t.title),
    muted(summary(t.description)),
    h(
      'dl',
      { class: 'facts' },
      h('div', {}, h('dt', {}, 'GPU'), h('dd', { class: 'mono' }, t.sku)),
      h('div', {}, h('dt', {}, 'Default runtime'), h('dd', {}, runtimeLabel(t.max_runtime_seconds))),
      h('div', {}, h('dt', {}, 'Inputs'), h('dd', {}, t.inputs.length ? t.inputs.map((m) => m.mount).join(', ') : 'none')),
      h('div', {}, h('dt', {}, 'Publisher'), h('dd', {}, t.publisher ?? PUBLISHER)),
    ),
    h('button', { type: 'button', class: 'secondary', 'data-run': t.id, disabled: !t.available, 'aria-label': `Run ${t.title}` }, t.available ? 'Run this' : t.unavailable_reason ?? 'Not runnable yet'),
  );
}

export async function marketplace() {
  const me = await shell();
  if (!me) return;
  $('access-note')!.hidden = me.account.status === 'active' && me.account.compute_enabled;

  const r = await get<List<Template>>('/templates');
  if (!r.ok) {
    fill('tpl-list', muted(notBuilt(r.error) ? 'Templates will appear here once the marketplace is live in the dashboard.' : r.error.message));
    return;
  }
  const templates = r.data.data;
  setText('tpl-count', String(templates.length));
  if (templates.length === 0) {
    fill('tpl-list', muted('No templates are offered yet.'));
    return;
  }
  const grid = h('ul', { class: 'tiles' }, ...templates.map(tile));
  fill('tpl-list', grid);
  const skus = await get<List<SKU>>('/skus');
  const sold = skus.ok ? skus.data.data.filter((k) => k.available) : [];

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
  function open(t: Template, from: HTMLElement) {
    current = t;
    opener = from;
    const can = (f: string) => t.overridable.includes(f);
    setText('run-title', `Run ${t.title}`);
    setText('run-sub', `${t.id} · ${t.description}`);
    const nodes: HTMLElement[] = [];
    t.inputs.forEach((m, i) => {
      nodes.push(
        field(`in-${i}`, `Input for ${m.mount}`, h('input', { type: 'url', inputmode: 'url', autocomplete: 'off', placeholder: 'https://…/file', required: true }), `${m.description} An https:// URL ending in a file name; the file is mounted at ${m.mount}.`),
      );
    });
    if (can('sku')) {
      const options = sold.some((k) => k.id === t.sku) ? sold : [{ id: t.sku, price_usd_per_hour: '', available: true }, ...sold];
      const select = h('select', {}, ...options.map((k) => h('option', { value: k.id, selected: k.id === t.sku }, k.price_usd_per_hour ? `${k.id} · USD ${k.price_usd_per_hour}/h` : k.id)));
      nodes.push(field('run-sku', 'GPU (SKU)', select, `The template runs on ${t.sku} unless you choose another.`));
      if (can('providers')) {
        const box = h('fieldset', { class: 'field', id: 'run-providers' }, h('legend', {}, 'Providers (optional)'));
        const render = () => {
          for (const el of Array.from(box.querySelectorAll('label'))) el.remove();
          const k = sold.find((x) => x.id === select.value);
          for (const p of k?.providers ?? []) {
            box.append(h('label', { class: 'check' }, h('input', { type: 'checkbox', name: 'provider', value: p.cloud }), p.cloud));
          }
          box.hidden = !(k?.providers ?? []).length;
        };
        select.addEventListener('change', render);
        render();
        nodes.push(box);
      }
    }
    if (can('spec.env')) {
      for (const [k, v] of Object.entries(t.env)) {
        nodes.push(field(`env-${k}`, `Environment: ${k}`, h('input', { type: 'text', autocomplete: 'off', value: v }), undefined));
        (nodes[nodes.length - 1].querySelector('input') as HTMLInputElement).value = v;
      }
    }
    if (can('spec.max_runtime_seconds')) {
      const n = h('input', { type: 'number', inputmode: 'numeric', min: '1', step: '1', autocomplete: 'off' });
      n.value = String(t.max_runtime_seconds);
      nodes.push(field('run-runtime', 'Maximum runtime (seconds)', n, 'The job is stopped at this limit, and the hold on your balance is sized by it.'));
    }
    if (can('spec.command')) {
      const ta = h('textarea', { rows: '4', spellcheck: 'false' });
      ta.value = t.command.join('\n');
      nodes.push(field('run-command', 'Command', ta, 'One argument per line, as the template\'s command is run in the image.'));
    }
    fields.replaceChildren(...nodes);
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
        setFieldError(`in-${i}`, 'Enter an https:// URL that ends in a file name.');
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
        setFieldError('run-runtime', 'Whole seconds, at least 1.');
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
      if (e.code === 'insufficient_balance') return setFormError(e.message, { href: '/billing', text: 'Top up' });
      if (e.code === 'compute_not_enabled') return setFormError(e.message, { href: CONTACT, text: 'Contact us' });
      return showApiError(e);
    }
    location.assign(`/jobs/${encodeURIComponent(r.data.id)}`);
    await new Promise(() => {});
  });
}
