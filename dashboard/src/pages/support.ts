import { t } from '../i18n';
import { $, busy, clearErrors, onSubmit, setFieldError, showApiError } from '../lib';
import { empty, fill, get, h, type List, muted, notBuilt, send, setText, shell, state, table, type Tone, when } from '../shell';

// The support conversation (ADR-0014; accounts.md "Support conversations").
// One conversation that is not closed per user: the form continues it, or
// starts one when there is none. Replies are polled while the page is open;
// the email is what tells someone who has left.

type Message = { id: number; from_staff: boolean; body: string; created_at: number };
type Conversation = {
  id: string;
  state: 'open' | 'answered' | 'closed';
  preview: string;
  created_at: number;
  updated_at: number;
  closed_at: number | null;
  messages?: Message[];
};

const STATE: Record<Conversation['state'], [string, Tone]> = {
  open: ['support.state-open', 'warning'],
  answered: ['support.state-answered', 'success'],
  closed: ['support.state-closed', 'muted'],
};
const MAX = 4000;
const POLL_MS = 30000;

const thread = (messages: Message[]) =>
  h(
    'ol',
    { class: 'chat' },
    ...messages.map((m) =>
      h(
        'li',
        { class: m.from_staff ? 'chat-msg from-staff' : 'chat-msg from-me' },
        h('p', { class: 'chat-meta' }, h('strong', {}, t(m.from_staff ? 'support.team' : 'support.you')), ' · ', when(m.created_at)),
        h('p', { class: 'chat-body', dir: 'auto' }, m.body),
      ),
    ),
  );

export async function support() {
  const me = await shell();
  if (!me) return;
  const form = $<HTMLFormElement>('write-form')!;
  const area = $<HTMLTextAreaElement>('message')!;
  const closeButton = $<HTMLButtonElement>('conv-close')!;
  const status = $('conv-status')!;
  let live: Conversation | null = null;

  const path = (c: Conversation) => `/support/conversations/${encodeURIComponent(c.id)}`;

  function show(c: Conversation | null) {
    live = c;
    closeButton.hidden = !c;
    if (!c) {
      fill('conv-state');
      fill('thread', empty(t('support.empty-title'), t('support.empty-text')));
      return;
    }
    fill('conv-state', state(t(STATE[c.state][0]), STATE[c.state][1]));
    fill('thread', thread(c.messages ?? []), muted(t(c.state === 'open' ? 'support.waiting-note' : 'support.answered-note')));
  }

  async function load() {
    const r = await get<List<Conversation>>('/support/conversations');
    if (!r.ok) {
      fill('thread', muted(notBuilt(r.error) ? t('support.will-appear') : r.error.message));
      return;
    }
    form.hidden = false;
    const current = r.data.data.find((c) => c.state !== 'closed');
    const past = r.data.data.filter((c) => c.state === 'closed');
    if (current) {
      const full = await get<Conversation>(path(current));
      show(full.ok ? full.data : current);
    } else show(null);
    $('earlier')!.hidden = past.length === 0;
    if (past.length)
      fill(
        'earlier-list',
        table(
          t('support.earlier'),
          [{ text: t('support.started') }, { text: t('support.last-message') }, { text: '' }],
          past.map((c) => [
            when(c.created_at),
            c.preview,
            h('button', { type: 'button', class: 'link', 'data-view': c.id, 'aria-label': t('support.view-from', { date: when(c.created_at) }) }, t('support.view')),
          ]),
        ),
      );
  }

  onSubmit(form, async () => {
    const message = area.value.trim();
    if (!message) {
      setFieldError('message', t('support.write-a-message'));
      return area.focus();
    }
    if ([...message].length > MAX) {
      setFieldError('message', t('support.too-long'));
      return area.focus();
    }
    busy(form, true);
    let r = live ? await send<Message>('POST', path(live) + '/messages', { message }) : await send<Conversation>('POST', '/support/conversations', { message });
    // Closed, or opened, in another tab since this page loaded: take the other route once.
    if (!r.ok && r.error.code === 'conversation_closed') r = await send<Conversation>('POST', '/support/conversations', { message });
    else if (!r.ok && r.error.code === 'conversation_open') {
      await load();
      if (live) r = await send<Message>('POST', path(live) + '/messages', { message });
    }
    if (!r.ok) return showApiError(r.error);
    area.value = '';
    clearErrors(form);
    status.textContent = t('support.sent');
    await load();
    area.focus();
  });

  closeButton.addEventListener('click', async () => {
    if (!live) return;
    closeButton.disabled = true;
    const r = await send<Conversation>('POST', path(live) + '/close');
    closeButton.disabled = false;
    if (!r.ok) return showApiError(r.error);
    status.textContent = t('support.closed-now');
    await load();
    area.focus();
  });

  $('earlier-list')!.addEventListener('click', async (e) => {
    const b = (e.target as Element).closest<HTMLButtonElement>('button[data-view]');
    if (!b) return;
    const r = await get<Conversation>(`/support/conversations/${encodeURIComponent(b.dataset.view!)}`);
    if (!r.ok) return showApiError(r.error);
    setText('past-title', t('support.past-title', { date: when(r.data.created_at) }));
    fill('past-thread', thread(r.data.messages ?? []));
    $('past')!.hidden = false;
    $('past-title')!.focus();
  });

  await load();

  setInterval(async () => {
    if (!live || document.visibilityState !== 'visible') return;
    const before = live;
    const r = await get<Conversation>(path(before));
    if (!r.ok || live !== before) return;
    const last = (c: Conversation) => c.messages?.at(-1)?.id ?? 0;
    if (r.data.state === 'closed') await load();
    else if (last(r.data) !== last(before) || r.data.state !== before.state) show(r.data);
  }, POLL_MS);
}
