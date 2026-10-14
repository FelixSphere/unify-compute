import { start, t } from './i18n';
// One script for every app-host page; body[data-page] picks the behaviour.
// Routes, bodies and error codes: docs/spec/accounts.md, "Dashboard API".

import {
  $,
  busy,
  call,
  checkConfirm,
  checkNewPassword,
  clearFieldError,
  focusFirstInvalid,
  input,
  onSubmit,
  setFieldError,
  setFormError,
  showApiError,
  wireConfirm,
  wireToggles,
} from './lib';
import { dashboard } from './dashboard';
import { esc } from './shell';
import { acceptInvitation } from './pages/accept-invitation';

const PENDING_EMAIL = 'uc_pending_email';
const honeypot = () => input('website')?.value ?? '';

// sessionStorage, not the URL: the address must not end up in a query string,
// browser history or a log. It can throw (blocked storage); the pages then ask.
const store = {
  get: () => {
    try {
      return sessionStorage.getItem(PENDING_EMAIL) ?? '';
    } catch {
      return '';
    }
  },
  set: (email: string) => {
    try {
      sessionStorage.setItem(PENDING_EMAIL, email);
    } catch {}
  },
  clear: () => {
    try {
      sessionStorage.removeItem(PENDING_EMAIL);
    } catch {}
  },
};

function checkEmail(): string | null {
  const email = input('email')?.value.trim() ?? '';
  if (!email) {
    setFieldError('email', t('auth.enter-your-email-address'));
    return null;
  }
  if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email)) {
    setFieldError('email', t('auth.enter-an-email-address-like'));
    return null;
  }
  return email;
}

const pages: Record<string, (form: HTMLFormElement | null) => void> = {
  signup(form) {
    if (!form) return;
    wireConfirm();
    onSubmit(form, async () => {
      const email = checkEmail();
      const pwOk = checkNewPassword();
      const confirmOk = checkConfirm();
      if (!email || !pwOk || !confirmOk) return focusFirstInvalid(form);
      busy(form, true);
      const r = await call('POST', '/signup', {
        email,
        password: input('password')!.value,
        password_confirm: input('password_confirm')!.value,
        website: honeypot(),
      });
      if (!r.ok) return showApiError(r.error);
      store.set(email);
      location.assign('/verify');
    });
  },

  login(form) {
    if (!form) return;
    onSubmit(form, async () => {
      const email = checkEmail();
      const password = input('password')?.value ?? '';
      if (email && !password) setFieldError('password', t('auth.enter-your-password'));
      if (!email || !password) return focusFirstInvalid(form);
      busy(form, true);
      const r = await call('POST', '/login', { email, password, website: honeypot() });
      if (r.ok) {
        store.clear();
        location.assign('/');
        return;
      }
      if (r.error.code === 'email_unverified') {
        store.set(email);
        return setFormError(r.error.message, { href: '/verify', text: t('auth.enter-your-code') });
      }
      showApiError(r.error);
    });
  },

  verify(form) {
    if (!form) return;
    const known = store.get();
    const emailField = $('email-field');
    const emailInput = input('email')!;
    if (known) {
      emailInput.value = known;
      if (emailField) emailField.hidden = true;
      const lead = $('lead');
      if (lead) {
        lead.innerHTML = t('verify.lead-known', { email: `<strong>${esc(known)}</strong>` });
      }
    }
    const code = input('code')!;
    // No maxlength on the field: it would cut "123 456", pasted from the
    // email, to "123 45" before the space is dropped here.
    code.addEventListener('input', () => {
      const digits = code.value.replace(/\D/g, '').slice(0, 6);
      if (digits !== code.value) code.value = digits;
    });
    // When the address is hidden and the server names it, show it again so
    // the error has somewhere to go.
    const reveal = () => {
      if (emailField) emailField.hidden = false;
    };
    onSubmit(form, async () => {
      if (emailField?.hidden === false && !checkEmail()) return focusFirstInvalid(form);
      const email = emailInput.value.trim();
      if (!email) {
        reveal();
        setFieldError('email', t('auth.enter-your-email-address'));
        return focusFirstInvalid(form);
      }
      if (!/^\d{6}$/.test(code.value)) {
        setFieldError('code', t('auth.enter-the-6-digits-from'));
        return focusFirstInvalid(form);
      }
      busy(form, true);
      const r = await call('POST', '/verify', { email, code: code.value });
      if (r.ok) {
        store.clear();
        location.assign('/');
        return;
      }
      if (r.error.param === 'email') reveal();
      showApiError(r.error.code === 'invalid_code' ? { ...r.error, param: 'code' } : r.error);
    });

    const resend = $<HTMLButtonElement>('resend');
    const status = $('status');
    resend?.addEventListener('click', async () => {
      const box = $('form-error');
      if (box) box.hidden = true;
      clearFieldError('email');
      const email = emailInput.value.trim();
      if (!email) {
        reveal();
        setFieldError('email', t('auth.enter-your-email-address-then'));
        emailInput.focus();
        return;
      }
      resend.disabled = true;
      const r = await call('POST', '/verify/resend', { email, website: honeypot() });
      if (!r.ok) {
        resend.disabled = false;
        if (r.error.param === 'email') reveal();
        return showApiError(r.error);
      }
      store.set(email);
      if (status) status.textContent = t('auth.if-that-address-is-waiting');
      // The server allows one resend a minute per address.
      setTimeout(() => {
        resend.disabled = false;
      }, 60_000);
    });
  },

  forgot(form) {
    if (!form) return;
    onSubmit(form, async () => {
      const email = checkEmail();
      if (!email) return focusFirstInvalid(form);
      busy(form, true);
      const r = await call('POST', '/password/forgot', { email, website: honeypot() });
      if (!r.ok) return showApiError(r.error);
      const sentEmail = $('sent-email');
      if (sentEmail) sentEmail.textContent = email;
      $('ask')!.hidden = true;
      $('sent')!.hidden = false;
      $('sent')!.querySelector<HTMLElement>('h1')?.focus();
    });
  },

  reset(form) {
    if (!form) return;
    // The token arrives in the fragment, which browsers never send to a
    // server or in Referer. Take it into memory and drop it from the address
    // bar and history at once, so it cannot be copied, synced or shared.
    const token = new URLSearchParams(location.hash.slice(1)).get('token') ?? '';
    if (location.hash) history.replaceState(null, '', location.pathname);
    const newLink = { href: '/forgot-password', text: t('auth.request-a-new-link') };
    if (!token) setFormError(t('auth.this-reset-link-is-incomplete'), newLink);
    wireConfirm();
    onSubmit(form, async () => {
      if (!token) {
        setFormError(t('auth.this-reset-link-is-incomplete'), newLink);
        return;
      }
      const pwOk = checkNewPassword();
      const confirmOk = checkConfirm();
      if (!pwOk || !confirmOk) return focusFirstInvalid(form);
      busy(form, true);
      const r = await call('POST', '/password/reset', {
        token,
        password: input('password')!.value,
        password_confirm: input('password_confirm')!.value,
      });
      if (!r.ok) return showApiError(r.error, r.error.code === 'invalid_token' ? newLink : undefined);
      $('ask')!.hidden = true;
      $('done')!.hidden = false;
      $('done')!.querySelector<HTMLElement>('h1')?.focus();
    });
  },
};

pages['accept-invitation'] = (form) => void acceptInvitation(form);

await start();
wireToggles();
const page = document.body.dataset.page ?? '';
if (page in dashboard) void dashboard[page]();
else pages[page]?.(document.querySelector<HTMLFormElement>('#form'));
