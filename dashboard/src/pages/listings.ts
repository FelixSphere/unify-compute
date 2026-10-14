import { $, busy, clearErrors, onSubmit, setFieldError, setFormError, showApiError } from '../lib';
import { CONTACT, fill, get, h, mono, muted, notBuilt, send, setText, shell, state, table, type Template, type Tone, when } from '../shell';

type Listing = {
  id: string;
  template_id: string;
  review_state: 'pending' | 'approved' | 'rejected';
  review_reason?: string;
  reviewed_at: number | null;
  listed: boolean;
  public: boolean;
  runs: number;
  template: Template;
  created_at: number;
  updated_at: number;
};
type ListingList = { data: Listing[]; open: boolean };

const LISTING_STATE: Record<Listing['review_state'], [string, Tone]> = {
  pending: ['In review', 'warning'],
  approved: ['Approved', 'success'],
  rejected: ['Rejected', 'danger'],
};

const SKELETON = {
  id: 'my-template',
  title: '',
  description: '',
  image: 'registry/image:tag@sha256:…',
  image_licence: '',
  image_source: '',
  command: ['python', 'run.py'],
  env: {},
  sku: 'a10g-24gb-x1',
  max_runtime_seconds: 3600,
  inputs: [{ mount: '/mnt/in', description: '' }],
  outputs: [{ mount: '/mnt/out', description: '' }],
  overridable: ['sku', 'spec.env', 'spec.max_runtime_seconds'],
  available: true,
};

export async function listings() {
  const me = await shell();
  if (!me) return;
  const openButton = $<HTMLButtonElement>('new-open')!;
  const card = $('edit')!;
  const form = $<HTMLFormElement>('edit-form')!;
  const area = $<HTMLTextAreaElement>('listing-json')!;
  const save = $<HTMLButtonElement>('edit-save')!;
  const status = $('lst-status')!;
  let editing: Listing | null = null;
  let opener: HTMLElement = openButton;

  async function load() {
    const r = await get<ListingList>('/listings');
    if (!r.ok) {
      setText('lst-count', '—');
      openButton.hidden = true;
      fill('lst-list', muted(notBuilt(r.error) ? 'Listings will appear here once the marketplace is live in the dashboard.' : r.error.message));
      return;
    }
    $('closed-note')!.hidden = r.data.open;
    const list = r.data.data;
    setText('lst-count', String(list.filter((l) => l.listed).length));
    if (list.length === 0) {
      fill('lst-list', muted('No listings yet. Submit a template to publish it.'));
      return;
    }
    fill(
      'lst-list',
      table(
        'Listings',
        [{ text: 'Template' }, { text: 'Review' }, { text: 'On the market' }, { text: 'Runs', num: true }, { text: 'Updated' }, { text: '' }],
        list.map((l) => {
          const [word, tone] = LISTING_STATE[l.review_state] ?? [l.review_state, 'muted'];
          const review = h('span', {}, state(word, tone), l.review_reason ? h('span', { class: 'reason' }, l.review_reason) : null);
          const actions = h('span', { class: 'row-actions' }, h('button', { type: 'button', class: 'link', 'data-edit': l.id, 'aria-label': `Edit ${l.template_id}` }, 'Edit'));
          if (l.listed) actions.append(' ', h('button', { type: 'button', class: 'link', 'data-unlist': l.id, 'data-display': l.template_id, 'aria-label': `Unlist ${l.template_id}` }, 'Unlist'));
          return [h('span', {}, mono(l.template_id), h('span', { class: 'reason' }, l.template.title)), review, l.public ? 'Live' : l.listed ? 'Listed, not live' : 'Unlisted', String(l.runs), when(l.updated_at), actions];
        }),
      ),
    );
    return list;
  }

  const closeEdit = () => {
    card.hidden = true;
    editing = null;
    openButton.hidden = false;
    opener.focus();
  };
  const openEdit = (l: Listing | null, from: HTMLElement) => {
    editing = l;
    opener = from;
    clearErrors(form);
    const t: Record<string, unknown> = l ? { ...l.template } : SKELETON;
    delete t.object;
    delete t.publisher;
    delete t.unavailable_reason;
    area.value = JSON.stringify(t, null, 2);
    setText('edit-title', l ? `Edit ${l.template_id}` : 'New listing');
    save.textContent = l ? 'Save and ask for review' : 'Submit for review';
    openButton.hidden = true;
    card.hidden = false;
    $('edit-title')!.focus();
  };
  openButton.addEventListener('click', () => openEdit(null, openButton));
  $('edit-cancel')!.addEventListener('click', closeEdit);

  onSubmit(form, async () => {
    let template: unknown;
    try {
      template = JSON.parse(area.value);
    } catch (e) {
      setFieldError('listing-json', `Not valid JSON: ${(e as Error).message}`);
      return area.focus();
    }
    if (!template || typeof template !== 'object' || Array.isArray(template)) {
      setFieldError('listing-json', 'The template must be a JSON object.');
      return area.focus();
    }
    busy(form, true);
    const r = editing ? await send<Listing>('PATCH', `/listings/${encodeURIComponent(editing.id)}`, { template }) : await send<Listing>('POST', '/listings', { template });
    if (!r.ok) {
      if (r.error.param?.startsWith('template')) {
        setFieldError('listing-json', r.error.message);
        return area.focus();
      }
      if (r.error.code === 'compute_not_enabled') return setFormError(r.error.message, { href: CONTACT, text: 'Contact us' });
      return showApiError(r.error);
    }
    status.textContent = editing ? `${r.data.template_id} saved; it is back in review.` : `${r.data.template_id} submitted for review.`;
    card.hidden = true;
    editing = null;
    openButton.hidden = false;
    await load();
    openButton.focus();
  });

  const dialog = $<HTMLDialogElement>('unlist-dialog')!;
  const dialogForm = $<HTMLFormElement>('unlist-form')!;
  const dialogError = $('unlist-error')!;
  let target: { id: string; opener: HTMLElement } | null = null;
  $('lst-list')!.addEventListener('click', async (e) => {
    const edit = (e.target as Element).closest<HTMLButtonElement>('button[data-edit]');
    if (edit) {
      const l = (await load())?.find((x) => x.id === edit.dataset.edit);
      if (l) openEdit(l, edit);
      return;
    }
    const b = (e.target as Element).closest<HTMLButtonElement>('button[data-unlist]');
    if (!b) return;
    target = { id: b.dataset.unlist!, opener: b };
    setText('unlist-display', b.dataset.display ?? '');
    dialogError.hidden = true;
    dialog.showModal();
  });
  dialogForm.addEventListener('submit', async (e) => {
    if (!target || dialogForm.returnValue === 'cancel' || (e.submitter as HTMLButtonElement | null)?.value !== 'unlist') return;
    e.preventDefault();
    const confirm = $<HTMLButtonElement>('unlist-confirm')!;
    confirm.disabled = true;
    const r = await send<Listing>('POST', `/listings/${encodeURIComponent(target.id)}/unlist`);
    confirm.disabled = false;
    if (!r.ok) {
      dialogError.querySelector('p')!.textContent = r.error.message;
      dialogError.hidden = false;
      return;
    }
    dialog.close();
    status.textContent = `${r.data.template_id} is off the market.`;
    await load();
    openButton.focus();
  });
  dialog.addEventListener('close', () => {
    target?.opener.focus();
    target = null;
  });

  await load();
}
