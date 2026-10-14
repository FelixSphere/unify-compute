import { $, busy, clearFieldError, focusFirstInvalid, GENERIC, input, onSubmit, setFieldError, setFormError, showApiError } from '../lib';
import { cents, type Child, empty, fill, get, h, jobLink, type List, listOf, money, mono, muted, notBuilt, send, setText, shell, showBalance, state, table, type Tone, usd, when } from '../shell';

type Topup = {
  id: string;
  amount_usd: string;
  status: string;
  created_at: number;
  paid_at: number | null;
  receipt_url: string | null;
  refunded_usd: string;
};
type TopupList = { data: Topup[]; topups_enabled?: boolean };
type Entry = {
  id: string;
  kind: string;
  amount_usd: string;
  available_after_usd: string;
  job_id?: string | null;
  ref: string | { type?: string; id?: string } | null;
  created_at: number;
};

const KIND: Record<string, string> = {
  topup: 'Top-up',
  hold: 'Hold',
  extend: 'Hold extended',
  settle: 'Charge',
  release: 'Released',
  refund: 'Refund',
  dispute: 'Dispute',
  dispute_reversal: 'Dispute reversed',
  admin_credit: 'Credit',
  admin_debit: 'Debit',
  writeoff: 'Write-off',
};

function refCell(ref: Entry['ref']): Child {
  const id = typeof ref === 'string' ? ref : ref?.id;
  if (!id) return '—';
  return id.startsWith('job_') ? jobLink(id) : mono(id);
}

const TOPUP_STATUS: Record<string, [string, Tone]> = {
  open: ['Awaiting payment', 'muted'],
  credited: ['Added', 'success'],
  expired: ['Not completed', 'muted'],
  failed: ['Failed', 'danger'],
  refunded: ['Refunded', 'muted'],
  partially_refunded: ['Partly refunded', 'muted'],
  disputed: ['Disputed', 'warning'],
  lost: ['Dispute lost', 'danger'],
};

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

export async function billing() {
  const me = await shell();
  if (!me) return;
  const balance = showBalance();
  const more = $<HTMLButtonElement>('history-more')!;
  const kindSel = $<HTMLSelectElement>('history-kind');
  let rows: Entry[] = [];
  let cursor: string | undefined;

  async function load(append: boolean) {
    more.disabled = true;
    const q = new URLSearchParams({ limit: '25' });
    for (const k of (kindSel?.value ?? '').split(',').filter(Boolean)) q.append('kind', k);
    if (append && cursor) q.set('after', cursor);
    const r = await get<Entry[] | List<Entry>>(`/transactions?${q}`);
    more.disabled = false;
    if (!r.ok) {
      more.hidden = true;
      fill(
        'history',
        muted(notBuilt(r.error) ? 'Your balance history will appear here: every credit, hold, charge and release on your account.' : r.error.message),
      );
      return;
    }
    const list = listOf(r.data);
    rows = append ? rows.concat(list.data) : list.data;
    cursor = list.has_more ? list.next : undefined;
    more.hidden = !cursor;
    fill(
      'history',
      rows.length
        ? table(
            'Balance history',
            [{ text: 'Date' }, { text: 'Entry' }, { text: 'Amount (USD)', num: true }, { text: 'Available after', num: true }, { text: 'Reference' }],
            rows.map((e) => [when(e.created_at), KIND[e.kind] ?? e.kind, money(e.amount_usd), money(e.available_after_usd), refCell(e.ref)]),
          )
        : empty('No balance history yet', 'Every credit, hold, charge and release on your account will be listed here.'),
    );
  }
  more.addEventListener('click', () => void load(true));
  kindSel?.addEventListener('change', () => void load(false));

  // --- Card top-ups (B1). The page never credits anything: it sends the
  // browser to Stripe's Checkout page and, on the way back, waits for the
  // webhook to have done it.
  const openButton = $<HTMLButtonElement>('topup-open')!;
  const contact = $('contact-primary')!;
  const card = $('topup')!;
  const off = $('topup-off')!;
  const form = $<HTMLFormElement>('topup-form')!;
  const amount = input('amount_usd')!;
  const status = $('topup-status')!;
  let min = 1000;
  let max = 10000;

  async function loadTopups(): Promise<TopupList | null> {
    const r = await get<TopupList>('/topups');
    if (!r.ok) return null;
    const list = r.data.data;
    $('topups-section')!.hidden = list.length === 0;
    fill(
      'topups',
      table(
        'Card top-ups',
        [{ text: 'Date' }, { text: 'Amount (USD)', num: true }, { text: 'Status' }, { text: 'Receipt' }],
        list.map((t) => [
          when(t.created_at),
          money(t.amount_usd),
          state(...(TOPUP_STATUS[t.status] ?? [t.status, 'muted'])),
          t.receipt_url && t.receipt_url.startsWith('https://') ? h('a', { href: t.receipt_url, rel: 'noopener noreferrer', target: '_blank' }, 'View receipt') : '—',
        ]),
      ),
    );
    return r.data;
  }

  const closeForm = () => {
    card.hidden = true;
    openButton.hidden = false;
  };
  openButton.addEventListener('click', () => {
    form.reset();
    clearFieldError('amount_usd');
    card.hidden = false;
    // One primary per view: the form's own replaces this one.
    openButton.hidden = true;
    amount.focus();
  });
  $('topup-cancel')!.addEventListener('click', () => {
    closeForm();
    openButton.focus();
  });

  onSubmit(form, async () => {
    const c = cents(amount.value);
    if (c === null) {
      setFieldError('amount_usd', 'Enter an amount in US dollars, like 20 or 20.00.');
      return focusFirstInvalid(form);
    }
    if (c < min || c > max) {
      setFieldError('amount_usd', `Enter an amount from USD ${(min / 100).toFixed(2)} to ${(max / 100).toFixed(2)}.`);
      return focusFirstInvalid(form);
    }
    busy(form, true);
    const r = await send<{ id: string; checkout_url: string }>('POST', '/topups', { amount_usd: (c / 100).toFixed(2) });
    if (!r.ok) return showApiError(r.error);
    if (!r.data.checkout_url.startsWith('https://')) return setFormError(GENERIC);
    // A full navigation to Stripe's hosted page: no Stripe script runs here,
    // and the CSP stays as strict as on every other page.
    busy(form, true);
    location.assign(r.data.checkout_url);
  });

  // Back from Checkout: /billing?topup=tu_…&checkout=success|cancelled.
  async function returned(id: string, outcome: string | null) {
    history.replaceState(null, '', '/billing');
    if (outcome === 'cancelled') {
      status.textContent = 'Payment cancelled. Nothing was charged.';
      return;
    }
    status.textContent = 'Payment received by Stripe. Adding it to your balance…';
    for (let i = 0; i < 15; i++) {
      const r = await get<Topup>(`/topups/${encodeURIComponent(id)}`);
      if (r.ok && r.data.status !== 'open') {
        status.textContent =
          r.data.status === 'credited' ? `USD ${usd(r.data.amount_usd)} added to your balance.` : 'This payment did not go through. Nothing was charged.';
        void showBalance();
        void loadTopups();
        void load(false);
        return;
      }
      if (!r.ok && r.error.status === 404) break;
      await sleep(2000);
    }
    status.textContent = 'Your payment is still being confirmed. It will appear in your balance within a few minutes; refresh this page to check.';
  }

  const q = new URLSearchParams(location.search);
  const back = q.get('topup');
  const [list] = await Promise.all([loadTopups(), load(false)]);
  if (list?.topups_enabled) {
    const b = await balance;
    min = cents(b?.limits?.topup_min_usd ?? '') ?? min;
    max = cents(b?.limits?.topup_max_usd ?? '') ?? max;
    setText('amount_usd-hint', `USD ${(min / 100).toFixed(2)} to ${(max / 100).toFixed(2)} per top-up. Your balance can hold up to USD ${b?.limits?.balance_max_usd ?? '200.00'}.`);
    off.hidden = true;
    contact.hidden = true;
    openButton.hidden = false;
  }
  if (back && /^tu_[0-9A-Z]{26}$/.test(back)) await returned(back, q.get('checkout'));
}
