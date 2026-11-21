import { t as tr } from '../i18n';
import { $, busy, clearFieldError, focusFirstInvalid, GENERIC, input, onSubmit, setFieldError, setFormError, showApiError } from '../lib';
import { type Balance, cents, type Child, empty, fill, get, h, jobLink, type List, listOf, lowBalanceBanner, money, mono, muted, notBuilt, send, setText, shell, showBalance, state, table, type Tone, usd, when } from '../shell';

type RefundRequest = {
  id: string;
  amount_usd: string;
  status: 'open' | 'refunded' | 'declined' | 'cancelled';
  resolution: string;
};
type Topup = {
  id: string;
  amount_usd: string;
  bonus_usd?: string;
  status: string;
  created_at: number;
  paid_at: number | null;
  receipt_url: string | null;
  refunded_usd: string;
  refundable_usd?: string;
  refund_request?: RefundRequest | null;
};
type BonusTier = { min_usd: string; percent: number };
type TopupList = { data: Topup[]; topups_enabled?: boolean; bonus_tiers?: BonusTier[] };
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
  topup_bonus: 'billing.kind-topup-bonus',
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

  // --- The low-balance alert (W6). Hidden on a compute-api that does not
  // report a threshold yet.
  const alertCard = $('alert')!;
  const alertForm = $<HTMLFormElement>('alert-form')!;
  const alertField = input('low_balance_usd')!;
  const alertStatus = $('alert-status')!;
  void balance.then((b) => {
    if (b?.low_balance_usd === undefined) {
      alertCard.hidden = true;
      return;
    }
    alertField.value = b.low_balance_usd;
  });
  onSubmit(alertForm, async () => {
    alertStatus.textContent = '';
    const c = cents(alertField.value);
    if (c === null || c > 1_000_000) {
      setFieldError('low_balance_usd', 'Enter an amount in US dollars from 0 to 10000, like 5 or 5.00.');
      return focusFirstInvalid(alertForm);
    }
    busy(alertForm, true);
    const r = await send<Balance>('PATCH', '/balance', { low_balance_usd: (c / 100).toFixed(2) });
    if (!r.ok) return showApiError(r.error);
    alertField.value = r.data.low_balance_usd ?? alertField.value;
    alertStatus.textContent = c === 0 ? 'Low-balance alert turned off.' : `Saved. We'll alert you below USD ${r.data.low_balance_usd}.`;
    lowBalanceBanner(r.data);
  });

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
  let tiers: BonusTier[] = [];

  // Mirrors internal/payments BonusFor: the highest tier the amount reaches,
  // rounded down to the cent. The server fixes the real bonus.
  function bonusFor(c: number): number {
    for (const t of tiers) if (c >= (cents(t.min_usd) ?? Infinity)) return Math.floor((c * t.percent) / 100);
    return 0;
  }

  // Only owners and admins can ask for a refund, as only they can top up.
  const canManage = me.user.role === undefined || me.user.role === 'owner' || me.user.role === 'admin';

  function refundCell(t: Topup): Child {
    const rr = t.refund_request;
    if (rr?.status === 'open')
      return h(
        'span',
        {},
        tr('billing.refund-requested', { amount: rr.amount_usd }),
        canManage ? ' · ' : '',
        canManage ? h('button', { type: 'button', class: 'link', 'data-withdraw': rr.id, 'aria-label': tr('billing.withdraw-label', { id: t.id }) }, tr('billing.withdraw')) : '',
      );
    const refundable = cents(t.refundable_usd ?? '') ?? 0;
    const last = rr?.status === 'declined' ? h('span', { class: 'muted', title: rr.resolution }, tr('billing.request-declined')) : null;
    if (refundable > 0 && canManage)
      return h(
        'span',
        {},
        last,
        last ? ' · ' : '',
        h(
          'button',
          { type: 'button', class: 'link', 'data-refund': t.id, 'data-refundable': t.refundable_usd, 'aria-label': tr('billing.request-refund-label', { id: t.id }) },
          tr('billing.request-refund'),
        ),
      );
    return last ?? '—';
  }

  async function loadTopups(): Promise<TopupList | null> {
    const r = await get<TopupList>('/topups');
    if (!r.ok) return null;
    const list = r.data.data;
    tiers = r.data.bonus_tiers ?? [];
    $('topups-section')!.hidden = list.length === 0;
    fill(
      'topups',
      table(
        tr('billing.card-top-ups'),
        [
          { text: tr('billing.date') },
          { text: tr('billing.amount-usd'), num: true },
          { text: tr('billing.bonus-usd'), num: true },
          { text: tr('common.status') },
          { text: tr('billing.receipt') },
          { text: tr('billing.refund') },
        ],
        list.map((t) => [
          when(t.created_at),
          money(t.amount_usd),
          t.bonus_usd && t.bonus_usd !== '0.00' ? money(t.bonus_usd) : '—',
          TOPUP_STATUS[t.status] ? state(tr(TOPUP_STATUS[t.status][0]), TOPUP_STATUS[t.status][1]) : state(t.status, 'muted'),
          t.receipt_url && t.receipt_url.startsWith('https://') ? h('a', { href: t.receipt_url, rel: 'noopener noreferrer', target: '_blank' }, tr('billing.view-receipt')) : '—',
          refundCell(t),
        ]),
      ),
    );
    return r.data;
  }

  // --- Refund requests (ADR-0013). The page only asks; the operator refunds
  // to the card, and the customer is emailed either way.
  const refundDialog = $<HTMLDialogElement>('refund-dialog')!;
  const refundAmount = input('refund-amount')!;
  const refundReason = $<HTMLTextAreaElement>('refund-reason')!;
  let refundTarget: { id: string; refundable: number } | null = null;
  $('topups')!.addEventListener('click', async (e) => {
    const b = (e.target as Element).closest<HTMLButtonElement>('button[data-refund], button[data-withdraw]');
    if (!b) return;
    if (b.dataset.withdraw) {
      b.disabled = true;
      const r = await send('DELETE', `/refund-requests/${encodeURIComponent(b.dataset.withdraw)}`);
      b.disabled = false;
      status.textContent = r.ok ? tr('billing.refund-withdrawn') : r.error.message;
      void loadTopups();
      return;
    }
    refundTarget = { id: b.dataset.refund!, refundable: cents(b.dataset.refundable ?? '') ?? 0 };
    setText('refund-amount-hint', tr('billing.refund-up-to', { amount: b.dataset.refundable ?? '', id: refundTarget.id }));
    refundAmount.value = b.dataset.refundable ?? '';
    refundReason.value = '';
    clearFieldError('refund-amount');
    clearFieldError('refund-reason');
    refundDialog.showModal();
    refundAmount.focus();
  });
  $<HTMLFormElement>('refund-form')!.addEventListener('submit', async (e) => {
    if (e.submitter?.getAttribute('value') !== 'send' || !refundTarget) return;
    e.preventDefault();
    const c = cents(refundAmount.value);
    if (c === null || c === 0 || c > refundTarget.refundable) {
      setFieldError('refund-amount', tr('billing.refund-amount-range', { max: (refundTarget.refundable / 100).toFixed(2) }));
      return refundAmount.focus();
    }
    const sendButton = $<HTMLButtonElement>('refund-send')!;
    sendButton.disabled = true;
    const r = await send<RefundRequest>('POST', `/topups/${encodeURIComponent(refundTarget.id)}/refund-request`, {
      amount_usd: (c / 100).toFixed(2),
      reason: refundReason.value.trim(),
    });
    sendButton.disabled = false;
    if (!r.ok) {
      const field = r.error.code === 'invalid_reason' ? 'refund-reason' : 'refund-amount';
      setFieldError(field, r.error.message);
      return (field === 'refund-reason' ? refundReason : refundAmount).focus();
    }
    refundDialog.close();
    status.textContent = tr('billing.refund-sent', { amount: r.data.amount_usd });
    await loadTopups();
    status.focus();
  });

  const closeForm = () => {
    card.hidden = true;
    openButton.hidden = false;
  };
  const showCredit = () => {
    const c = cents(amount.value);
    const bonus = c !== null && c >= min && c <= max ? bonusFor(c) : 0;
    setText('amount_usd-credit', bonus > 0 ? tr('billing.credit-preview', { total: ((c! + bonus) / 100).toFixed(2), bonus: (bonus / 100).toFixed(2) }) : '');
  };
  amount.addEventListener('input', showCredit);
  openButton.addEventListener('click', () => {
    form.reset();
    showCredit();
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
          r.data.status !== 'credited'
            ? tr('billing.this-payment-did-not-go')
            : r.data.bonus_usd && r.data.bonus_usd !== '0.00'
              ? tr('billing.added-to-balance-bonus', { amount: usd(r.data.amount_usd), bonus: usd(r.data.bonus_usd) })
              : tr('billing.added-to-balance', { amount: usd(r.data.amount_usd) });
        void showBalance(true);
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
    const reachable = tiers.filter((t) => (cents(t.min_usd) ?? Infinity) <= max).reverse();
    if (reachable.length) {
      setText('bonus-note', tr('billing.bonus-note', { tiers: reachable.map((t) => tr('billing.bonus-tier', { percent: t.percent, min: t.min_usd })).join(', ') }));
      $('bonus-note')!.hidden = false;
    }
    off.hidden = true;
    contact.hidden = true;
    openButton.hidden = false;
  }
  if (back && /^tu_[0-9A-Z]{26}$/.test(back)) await returned(back, q.get('checkout'));
}
