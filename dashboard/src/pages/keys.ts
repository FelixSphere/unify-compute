import { $, busy, clearFieldError, focusFirstInvalid, input, onSubmit, setFieldError, showApiError } from '../lib';
import { cents, fill, get, h, type List, listOf, mono, muted, notBuilt, send, setText, shell, state, table, when, withReauth } from '../shell';

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
      setText('keys-count', '—');
      fill('keys-list', muted(notBuilt(r.error) ? 'Your API keys will be listed here once keys are live in the dashboard.' : r.error.message));
      return;
    }
    const list = listOf(r.data).data;
    setText('keys-count', String(list.filter(active).length));
    if (list.length === 0) {
      fill('keys-list', muted('No keys yet. Create one to submit jobs.'));
      return;
    }
    fill(
      'keys-list',
      table(
        'API keys',
        [{ text: 'Name' }, { text: 'Key' }, { text: 'Access' }, { text: 'This month / cap (USD)', num: true }, { text: 'Created' }, { text: 'Last used' }, { text: 'Status' }, { text: '' }],
        list.map((k) => {
          const live = active(k);
          const st = k.revoked_at ? state('Revoked', 'muted') : live ? state('Active', 'success') : state('Expired', 'muted');
          const spent = k.spent_this_month_usd ?? '—';
          const capCell = h(
            'span',
            { class: 'mono' },
            `${spent} / ${k.monthly_cap_usd ?? 'no cap'}`,
          );
          const actions = h('span', { class: 'row-actions' });
          if (live) {
            actions.append(
              h('button', { type: 'button', class: 'link', 'data-cap': String(k.id), 'data-display': k.display, 'data-cap-value': k.monthly_cap_usd ?? '', 'data-spent': spent, 'aria-label': `Set the monthly cap of ${k.name}` }, 'Cap'),
              ' ',
              h('button', { type: 'button', class: 'link', 'data-revoke': String(k.id), 'data-display': k.display, 'aria-label': `Revoke ${k.name}` }, 'Revoke'),
            );
          }
          return [k.name, mono(k.display), k.scopes.includes('jobs') ? 'Jobs' : 'Read only', capCell, when(k.created_at), when(k.last_used_at), st, actions];
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
      setFieldError('key-name', 'Give the key a name.');
      return focusFirstInvalid(form);
    }
    const scope = form.querySelector<HTMLInputElement>('input[name=scope]:checked')?.value ?? 'jobs';
    const capField = input('key-cap')!;
    const cap = capField.value.trim();
    if (cap && cents(cap) === null) {
      setFieldError('key-cap', 'Dollars with at most two decimals, e.g. 20 or 12.50.');
      return capField.focus();
    }
    busy(form, true);
    const r = await withReauth(() =>
      send<Key & { secret: string }>('POST', '/keys', {
        name,
        scopes: scope === 'jobs' ? ['jobs', 'read'] : ['read'],
        monthly_cap_usd: cap || null,
      }),
    );
    if (!r) return;
    if (!r.ok) {
      if (r.error.param === 'name') {
        setFieldError('key-name', r.error.message);
        return nameField.focus();
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
      status.textContent = 'Copied to the clipboard.';
    } catch {
      // Clipboard access refused: select the key so a keyboard copy works.
      const range = document.createRange();
      range.selectNodeContents($('reveal-secret')!);
      const sel = getSelection();
      sel?.removeAllRanges();
      sel?.addRange(range);
      status.textContent = "Couldn't copy automatically. The key is selected: press Ctrl+C or ⌘C.";
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
      setFieldError('cap-value', 'Dollars with at most two decimals, e.g. 20 or 12.50.');
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
    $('keys-list')!.prepend(h('p', { class: 'status-line', role: 'status' }, v ? `Monthly cap set to USD ${r.data.monthly_cap_usd}.` : 'Monthly cap removed.'));
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
    announce.prepend(h('p', { class: 'status-line', role: 'status' }, `Key ${target.display} revoked.`));
    $('main')?.focus();
  });

  await load();
}
