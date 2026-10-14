import { t as tr } from '../i18n';
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

// Catalogue keys, read with tr() at render time: a module-level string would
// be English, since the catalogue loads after this module.
const KIND: Record<string, string> = {
  topup: 'billing.kind-topup',
  hold: 'billing.kind-hold',
  extend: 'billing.kind-extend',
  settle: 'billing.kind-settle',
  release: 'billing.kind-release',
  refund: 'billing.kind-refund',
  dispute: 'billing.kind-dispute',
  dispute_reversal: 'billing.kind-dispute-reversal',
  admin_credit: 'billing.kind-admin-credit',
  admin_debit: 'billing.kind-admin-debit',
  writeoff: 'billing.kind-writeoff',
};

function refCell(ref: Entry['ref']): Child {
  const id = typeof ref === 'string' ? ref : ref?.id;
  if (!id) return '—';
  return id.startsWith('job_') ? jobLink(id) : mono(id);
}

const TOPUP_STATUS: Record<string, [string, Tone]> = {
  open: ['billing.topup-open', 'muted'],
  credited: ['billing.topup-credited', 'success'],
  expired: ['billing.topup-expired', 'muted'],
  failed: ['billing.topup-failed', 'danger'],
  refunded: ['billing.topup-refunded', 'muted'],
  partially_refunded: ['billing.topup-partially-refunded', 'muted'],
  disputed: ['billing.topup-disputed', 'warning'],
  lost: ['billing.topup-lost', 'danger'],
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
        muted(notBuilt(r.error) ? tr('billing.your-balance-history-will-appear') : r.error.message),
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
            tr('billing.balance-history'),
            [{ text: tr('billing.date') }, { text: tr('billing.entry') }, { text: tr('billing.amount-usd'), num: true }, { text: tr('billing.available-after'), num: true }, { text: tr('billing.reference') }],
            rows.map((e) => [when(e.created_at), KIND[e.kind] ? tr(KIND[e.kind]) : e.kind, money(e.amount_usd), money(e.available_after_usd), refCell(e.ref)]),
          )
        : empty(tr('billing.no-balance-history-yet'), tr('billing.every-credit-hold-charge-and')),
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
        tr('billing.card-top-ups'),
        [{ text: tr('billing.date') }, { text: tr('billing.amount-usd'), num: true }, { text: tr('common.status') }, { text: tr('billing.receipt') }],
        list.map((t) => [
          when(t.created_at),
          money(t.amount_usd),
          TOPUP_STATUS[t.status] ? state(tr(TOPUP_STATUS[t.status][0]), TOPUP_STATUS[t.status][1]) : state(t.status, 'muted'),
          t.receipt_url && t.receipt_url.startsWith('https://') ? h('a', { href: t.receipt_url, rel: 'noopener noreferrer', target: '_blank' }, tr('billing.view-receipt')) : '—',
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
      setFieldError('amount_usd', tr('billing.enter-an-amount-in-us'));
      return focusFirstInvalid(form);
    }
    if (c < min || c > max) {
      setFieldError('amount_usd', tr('billing.amount-range', { min: (min / 100).toFixed(2), max: (max / 100).toFixed(2) }));
      return focusFirstInvalid(form);
    }
    busy(form, true);
    const r = await send<{ id: string; checkout_url: string }>('POST', '/topups', { amount_usd: (c / 100).toFixed(2) });
    if (!r.ok) return showApiError(r.error);
    if (!r.data.checkout_url.startsWith('https://')) return setFormError(GENERIC());
    // A full navigation to Stripe's hosted page: no Stripe script runs here,
    // and the CSP stays as strict as on every other page.
    busy(form, true);
    location.assign(r.data.checkout_url);
  });

  // Back from Checkout: /billing?topup=tu_…&checkout=success|cancelled.
  async function returned(id: string, outcome: string | null) {
    history.replaceState(null, '', '/billing');
    if (outcome === 'cancelled') {
      status.textContent = tr('billing.payment-cancelled-nothing-was-charged');
      return;
    }
    status.textContent = tr('billing.payment-received-by-stripe-adding');
    for (let i = 0; i < 15; i++) {
      const r = await get<Topup>(`/topups/${encodeURIComponent(id)}`);
      if (r.ok && r.data.status !== 'open') {
        status.textContent =
          r.data.status === 'credited' ? tr('billing.added-to-balance', { amount: usd(r.data.amount_usd) }) : tr('billing.this-payment-did-not-go');
        void showBalance();
        void loadTopups();
        void load(false);
        return;
      }
      if (!r.ok && r.error.status === 404) break;
      await sleep(2000);
    }
    status.textContent = tr('billing.your-payment-is-still-being');
  }

  const q = new URLSearchParams(location.search);
  const back = q.get('topup');
  const [list] = await Promise.all([loadTopups(), load(false)]);
  if (list?.topups_enabled) {
    const b = await balance;
    min = cents(b?.limits?.topup_min_usd ?? '') ?? min;
    max = cents(b?.limits?.topup_max_usd ?? '') ?? max;
    setText('amount_usd-hint', tr('billing.topup-limits', { min: (min / 100).toFixed(2), max: (max / 100).toFixed(2), cap: b?.limits?.balance_max_usd ?? '200.00' }));
    off.hidden = true;
    contact.hidden = true;
    openButton.hidden = false;
  }
  if (back && /^tu_[0-9A-Z]{26}$/.test(back)) await returned(back, q.get('checkout'));
}
