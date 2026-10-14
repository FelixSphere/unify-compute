import { t } from '../i18n';
import { $, busy, clearFieldError, focusFirstInvalid, input, onSubmit, setFieldError, showApiError } from '../lib';
import { cents, empty, fill, get, h, type List, listOf, mono, muted, notBuilt, send, setText, shell, state, table, usd, when, withReauth } from '../shell';

type Key = {
  id: number | string;
  name: string;
  display: string;
  scopes: string[];
  created_at: number;
  expires_at: number | null;
  last_used_at: number | null;
  revoked_at: number | null;
  monthly_cap_usd?: string | null;
  spent_this_month_usd?: string | null;
};

// A date input's YYYY-MM-DD as the last second of that local day, so a key
// set to expire "on the 31st" works through all of it and the Expires column
// shows the 31st.
function endOfDay(date: string): number | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date);
  if (!m) return null;
  const next = new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]) + 1);
  return Number.isNaN(next.getTime()) ? null : Math.floor(next.getTime() / 1000) - 1;
}

export async function keys() {
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
      setText('keys-count', '');
      fill('keys-list', muted(notBuilt(r.error) ? t('keys.your-api-keys-will-be') : r.error.message));
      return;
    }
    const list = listOf(r.data).data;
    const n = list.filter(active).length;
    setText('keys-count', t('keys.active-of-10', { n }));
    if (list.length === 0) {
      fill('keys-list', empty(t('keys.no-keys-yet'), t('keys.create-a-key-to-submit')));
      return;
    }
    fill(
      'keys-list',
      table(
        t('nav.keys'),
        [{ text: t('keys.key') }, { text: t('keys.access') }, { text: t('keys.this-month-cap-usd'), num: true }, { text: t('keys.expires-2') }, { text: t('keys.last-used') }, { text: t('common.status') }, { text: '' }],
        list.map((k) => {
          const live = active(k);
          const st = k.revoked_at ? state(t('keys.revoked'), 'muted') : live ? state(t('keys.active'), 'success') : state(t('keys.expired'), 'muted');
          const spent = k.spent_this_month_usd ?? '—';
          const capCell = h('span', { class: 'mono' }, `${usd(spent)} / ${k.monthly_cap_usd ? usd(k.monthly_cap_usd) : t('keys.no-cap')}`);
          const nameCell = h('span', { class: 'cell-stack' }, h('strong', {}, k.name), mono(k.display));
          const actions = h('span', { class: 'row-actions' });
          if (live) {
            actions.append(
              h('button', { type: 'button', class: 'link', 'data-cap': String(k.id), 'data-display': k.display, 'data-cap-value': k.monthly_cap_usd ?? '', 'data-spent': spent, 'aria-label': t('keys.set-cap-of', { name: k.name }) }, t('keys.cap')),
              ' ',
              h('button', { type: 'button', class: 'link', 'data-revoke': String(k.id), 'data-display': k.display, 'aria-label': t('keys.revoke-name', { name: k.name }) }, t('keys.revoke')),
            );
          }
          return [nameCell, k.scopes.includes('jobs') ? t('nav.jobs') : t('keys.read-only'), capCell, k.expires_at ? when(k.expires_at) : t('keys.never'), k.last_used_at ? when(k.last_used_at) : t('keys.never'), st, actions];
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
    const today = new Date();
    input('key-expires')!.min = `${today.getFullYear()}-${String(today.getMonth() + 1).padStart(2, '0')}-${String(today.getDate()).padStart(2, '0')}`;
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
      setFieldError('key-name', t('keys.give-the-key-a-name'));
      return focusFirstInvalid(form);
    }
    const scope = form.querySelector<HTMLInputElement>('input[name=scope]:checked')?.value ?? 'jobs';
    const capField = input('key-cap')!;
    const cap = capField.value.trim();
    if (cap && cents(cap) === null) {
      setFieldError('key-cap', t('keys.dollars-with-at-most-two'));
      return capField.focus();
    }
    const expiresField = input('key-expires')!;
    let expiresAt: number | null = null;
    if (expiresField.value) {
      expiresAt = endOfDay(expiresField.value);
      if (expiresAt === null || expiresAt * 1000 <= Date.now()) {
        setFieldError('key-expires', t('keys.choose-today-or-a-later'));
        return expiresField.focus();
      }
    }
    busy(form, true);
    const r = await withReauth(() =>
      send<Key & { secret: string }>('POST', '/keys', {
        name,
        scopes: scope === 'jobs' ? ['jobs', 'read'] : ['read'],
        expires_at: expiresAt,
        monthly_cap_usd: cap || null,
      }),
    );
    if (!r) return;
    if (!r.ok) {
      if (r.error.param === 'name') {
        setFieldError('key-name', r.error.message);
        return nameField.focus();
      }
      if (r.error.param === 'expires_at') {
        setFieldError('key-expires', r.error.message);
        return expiresField.focus();
      }
      if (r.error.param === 'monthly_cap_usd') {
        setFieldError('key-cap', r.error.message);
        return capField.focus();
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
      status.textContent = t('keys.copied-to-the-clipboard');
    } catch {
      // Clipboard access refused: select the key so a keyboard copy works.
      const range = document.createRange();
      range.selectNodeContents($('reveal-secret')!);
      const sel = getSelection();
      sel?.removeAllRanges();
      sel?.addRange(range);
      status.textContent = t('keys.couldn-t-copy-automatically-the');
    }
  });
  $('reveal-done')!.addEventListener('click', () => {
    $('reveal-secret')!.textContent = '';
    getSelection()?.removeAllRanges();
    reveal.hidden = true;
    openButton.hidden = false;
    openButton.focus();
  });

  // Cap: a small dialog with one field; PATCH /keys/{id} sets or clears it.
  const capDialog = $<HTMLDialogElement>('cap-dialog')!;
  const capValue = input('cap-value')!;
  let capTarget: { id: string; opener: HTMLElement } | null = null;
  $('keys-list')!.addEventListener('click', (e) => {
    const b = (e.target as Element).closest<HTMLButtonElement>('button[data-cap]');
    if (!b) return;
    capTarget = { id: b.dataset.cap!, opener: b };
    setText('cap-display', b.dataset.display ?? '');
    setText('cap-spent', b.dataset.spent ?? '—');
    capValue.value = b.dataset.capValue ?? '';
    clearFieldError('cap-value');
    capDialog.showModal();
    capValue.focus();
  });
  $<HTMLFormElement>('cap-form')!.addEventListener('submit', async (e) => {
    if (e.submitter?.getAttribute('value') !== 'save' || !capTarget) return;
    e.preventDefault();
    const v = capValue.value.trim();
    if (v && cents(v) === null) {
      setFieldError('cap-value', t('keys.dollars-with-at-most-two'));
      return capValue.focus();
    }
    const save = $<HTMLButtonElement>('cap-save')!;
    save.disabled = true;
    const r = await withReauth(() => send<Key>('PATCH', `/keys/${encodeURIComponent(capTarget!.id)}`, { monthly_cap_usd: v || null }));
    save.disabled = false;
    if (!r) return;
    if (!r.ok) {
      setFieldError('cap-value', r.error.message);
      return capValue.focus();
    }
    capDialog.close();
    await load();
    $('keys-list')!.prepend(h('p', { class: 'status-line', role: 'status' }, v ? t('keys.cap-set', { amount: usd(r.data.monthly_cap_usd) }) : t('keys.monthly-cap-removed')));
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
    announce.prepend(h('p', { class: 'status-line', role: 'status' }, t('keys.key-revoked', { key: target.display })));
    $('main')?.focus();
  });

  await load();
}
