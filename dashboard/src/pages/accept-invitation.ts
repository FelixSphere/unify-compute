// /accept-invitation#token=… (spec/accounts.md, "Public"). The link proves
// the address, so the invitee only chooses a password; there is no
// verification code. The token never leaves the fragment except in a POST
// body: not in the address bar, history, Referer or an access log.

import { $, busy, call, checkConfirm, checkNewPassword, focusFirstInvalid, input, onSubmit, setFormError, showApiError, wireConfirm } from '../lib';

type Lookup = { account_name: string; email: string; role: 'admin' | 'member'; invited_by_email: string; expires_at: number };

const ROLE = { admin: 'Admin', member: 'Member' } as const;
const dateFmt = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' });

export async function acceptInvitation(form: HTMLFormElement | null) {
  if (!form) return;
  const token = new URLSearchParams(location.hash.slice(1)).get('token') ?? '';
  if (location.hash) history.replaceState(null, '', location.pathname);
  const lead = $('lead')!;
  const gone = (message: string) => {
    $('title')!.textContent = 'This invitation cannot be used';
    lead.textContent = message;
  };
  if (!token) {
    gone('This invitation link is incomplete. Open the link from the email again.');
    return;
  }
  const r = await call<Lookup>('POST', '/invitations/lookup', { token });
  if (!r.ok) {
    if (r.error.status === 404 && r.error.code === 'not_found') return gone(r.error.message);
    lead.textContent = '';
    return setFormError(r.error.message);
  }
  const inv = r.data;
  $('title')!.textContent = `Join ${inv.account_name}`;
  lead.textContent = '';
  const strong = (t: string) => {
    const s = document.createElement('strong');
    s.textContent = t;
    return s;
  };
  lead.append(
    ...(inv.invited_by_email ? [strong(inv.invited_by_email), ' invited you'] : ['You were invited']),
    ' to ',
    strong(inv.account_name),
    ` on Unify Compute as ${ROLE[inv.role] ?? inv.role}. Members share the account's balance, API keys and jobs. The invitation expires ${dateFmt.format(new Date(inv.expires_at * 1000))}.`,
  );
  input('email')!.value = inv.email;
  form.hidden = false;
  wireConfirm();
  input('password')!.focus();

  onSubmit(form, async () => {
    const pwOk = checkNewPassword();
    const confirmOk = checkConfirm();
    if (!pwOk || !confirmOk) return focusFirstInvalid(form);
    busy(form, true);
    const a = await call('POST', '/invitations/accept', {
      token,
      password: input('password')!.value,
      password_confirm: input('password_confirm')!.value,
    });
    if (a.ok) {
      location.assign('/');
      return;
    }
    if (a.error.code === 'email_in_use') return setFormError(a.error.message, { href: '/login', text: 'Log in' });
    if (a.error.status === 404) {
      form.hidden = true;
      return gone(a.error.message);
    }
    showApiError(a.error);
  });
}
