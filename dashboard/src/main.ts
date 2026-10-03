// One script for every app-host page; body[data-page] picks the behaviour.
// Routes, bodies and error codes: docs/spec/accounts.md, "Dashboard API".

type ApiError = { status: number; code: string; message: string; param?: string };
type Result<T> = { ok: true; status: number; data: T } | { ok: false; error: ApiError };

const API = '/dashboard/v1';
const PENDING_EMAIL = 'uc_pending_email';
const MIN_PASSWORD = 12;
const GENERIC = "Something went wrong on our side. Try again in a minute.";

async function call<T>(method: string, path: string, body?: unknown, csrf?: string): Promise<Result<T>> {
  const headers: Record<string, string> = { Accept: 'application/json' };
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  if (csrf) headers['X-CSRF-Token'] = csrf;
  let res: Response;
  try {
    res = await fetch(API + path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: 'same-origin',
      cache: 'no-store',
    });
  } catch {
    return { ok: false, error: { status: 0, code: 'network', message: "We couldn't reach Unify Compute. Check your connection and try again." } };
  }
  const json = await res.json().catch(() => null);
  if (res.ok) return { ok: true, status: res.status, data: json as T };
  const e = json?.error;
  if (e && typeof e.code === 'string') {
    let message = typeof e.message === 'string' && e.message ? e.message : GENERIC;
    const retry = res.headers.get('Retry-After');
    if (res.status === 429 && retry && /^\d+$/.test(retry) && !/second|minute/.test(message)) {
      message += ` Try again in ${retry} seconds.`;
    }
    return { ok: false, error: { status: res.status, code: e.code, message, param: e.param || undefined } };
  }
  return { ok: false, error: { status: res.status, code: 'unexpected', message: GENERIC } };
}

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

const $ = <T extends HTMLElement>(id: string) => document.getElementById(id) as T | null;
const input = (id: string) => $<HTMLInputElement>(id);

function setFieldError(name: string, message: string) {
  const el = input(name);
  const err = $(`${name}-error`);
  if (!el || !err) return false;
  el.setAttribute('aria-invalid', 'true');
  err.textContent = message;
  err.hidden = false;
  return true;
}

function clearFieldError(name: string) {
  input(name)?.removeAttribute('aria-invalid');
  const err = $(`${name}-error`);
  if (err) {
    err.textContent = '';
    err.hidden = true;
  }
}

function setFormError(message: string, link?: { href: string; text: string }) {
  const box = $('form-error');
  const p = box?.querySelector('p');
  if (!box || !p) return;
  p.textContent = message;
  if (link) {
    const a = document.createElement('a');
    a.href = link.href;
    a.textContent = link.text;
    p.append(' ', a);
  }
  box.hidden = false;
  box.focus();
}

function clearErrors(form: HTMLFormElement) {
  const box = $('form-error');
  if (box) box.hidden = true;
  for (const el of Array.from(form.elements)) {
    if (el instanceof HTMLInputElement && el.id) clearFieldError(el.id);
  }
}

// A field named by `param` gets the message under it; anything else goes in
// the box at the top of the card (UI-STANDARD, "Errors").
function showApiError(e: ApiError, link?: { href: string; text: string }) {
  if (e.param && setFieldError(e.param, e.message)) {
    input(e.param)?.focus();
    return;
  }
  setFormError(e.message, link);
}

function focusFirstInvalid(form: HTMLFormElement) {
  form.querySelector<HTMLInputElement>('input[aria-invalid="true"]')?.focus();
}

function busy(form: HTMLFormElement, on: boolean) {
  const button = form.querySelector<HTMLButtonElement>('button[type="submit"]');
  if (!button) return;
  button.disabled = on;
  button.setAttribute('aria-busy', String(on));
}

function checkEmail(): string | null {
  const email = input('email')?.value.trim() ?? '';
  if (!email) {
    setFieldError('email', 'Enter your email address.');
    return null;
  }
  if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email)) {
    setFieldError('email', 'Enter an email address like name@example.com.');
    return null;
  }
  return email;
}

function checkNewPassword(): boolean {
  const pw = input('password')?.value ?? '';
  if (pw.length < MIN_PASSWORD) {
    setFieldError('password', `Use a password of at least ${MIN_PASSWORD} characters.`);
    return false;
  }
  return true;
}

const MISMATCH = "Passwords don't match.";

function checkConfirm(): boolean {
  const pw = input('password')?.value ?? '';
  const confirm = input('password_confirm')?.value ?? '';
  if (!confirm) {
    setFieldError('password_confirm', 'Enter the password again.');
    return false;
  }
  if (pw !== confirm) {
    setFieldError('password_confirm', MISMATCH);
    return false;
  }
  clearFieldError('password_confirm');
  return true;
}

// "Passwords don't match." on blur and on submit; it clears as soon as the two
// agree, so the error never outlives the mistake.
function wireConfirm() {
  const confirm = input('password_confirm');
  const pw = input('password');
  if (!confirm || !pw) return;
  confirm.addEventListener('blur', () => {
    if (confirm.value) checkConfirm();
  });
  const recheck = () => {
    if (confirm.getAttribute('aria-invalid') === 'true' && confirm.value === pw.value) clearFieldError('password_confirm');
  };
  confirm.addEventListener('input', recheck);
  pw.addEventListener('input', recheck);
}

function wireToggles() {
  for (const button of Array.from(document.querySelectorAll<HTMLButtonElement>('button.toggle'))) {
    const target = input(button.getAttribute('aria-controls') ?? '');
    if (!target) continue;
    button.addEventListener('click', () => {
      const show = target.type === 'password';
      target.type = show ? 'text' : 'password';
      button.setAttribute('aria-pressed', String(show));
      button.setAttribute('aria-label', show ? 'Hide password' : 'Show password');
    });
  }
}

function wireClearOnInput(form: HTMLFormElement) {
  for (const el of Array.from(form.querySelectorAll<HTMLInputElement>('input'))) {
    if (el.id === 'password_confirm') continue;
    el.addEventListener('input', () => {
      if (el.getAttribute('aria-invalid') === 'true') clearFieldError(el.id);
    });
  }
}

function onSubmit(form: HTMLFormElement, handler: () => Promise<void>) {
  wireClearOnInput(form);
  form.addEventListener('submit', async (ev) => {
    ev.preventDefault();
    if (form.getAttribute('aria-busy') === 'true') return;
    clearErrors(form);
    form.setAttribute('aria-busy', 'true');
    try {
      await handler();
    } finally {
      form.removeAttribute('aria-busy');
      busy(form, false);
    }
  });
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
        website: input('website')?.value ?? '',
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
      if (email && !password) setFieldError('password', 'Enter your password.');
      if (!email || !password) return focusFirstInvalid(form);
      busy(form, true);
      const r = await call('POST', '/login', { email, password });
      if (r.ok) {
        store.clear();
        location.assign('/');
        return;
      }
      if (r.error.code === 'email_unverified') {
        store.set(email);
        return setFormError(r.error.message, { href: '/verify', text: 'Enter your code' });
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
        lead.textContent = 'Enter the 6-digit code we sent to ';
        const strong = document.createElement('strong');
        strong.textContent = known;
        lead.append(strong, '. It expires in 15 minutes.');
      }
    }
    const code = input('code')!;
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
        setFieldError('email', 'Enter your email address.');
        return focusFirstInvalid(form);
      }
      if (!/^\d{6}$/.test(code.value)) {
        setFieldError('code', 'Enter the 6 digits from the email.');
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
        setFieldError('email', 'Enter your email address, then resend.');
        emailInput.focus();
        return;
      }
      resend.disabled = true;
      const r = await call('POST', '/verify/resend', { email });
      if (!r.ok) {
        resend.disabled = false;
        if (r.error.param === 'email') reveal();
        return showApiError(r.error);
      }
      store.set(email);
      if (status) status.textContent = 'If that address is waiting for verification, we sent it a new code.';
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
      const r = await call('POST', '/password/forgot', { email });
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
    const newLink = { href: '/forgot-password', text: 'Request a new link' };
    if (!token) setFormError('This reset link is incomplete. Open the link from the email again, or request a new one.', newLink);
    wireConfirm();
    onSubmit(form, async () => {
      if (!token) {
        setFormError('This reset link is incomplete. Open the link from the email again, or request a new one.', newLink);
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

type Me = { user: { email: string }; csrf_token: string };

async function home() {
  const loading = $('loading');
  const r = await call<Me>('GET', '/me');
  if (!r.ok) {
    if (r.error.status === 401) {
      location.replace('/login');
      return;
    }
    if (loading) loading.hidden = true;
    setFormError(r.error.message);
    return;
  }
  if (loading) loading.hidden = true;
  $('account-email')!.textContent = r.data.user.email;
  $('account')!.hidden = false;
  const logout = $<HTMLButtonElement>('logout')!;
  logout.hidden = false;
  logout.addEventListener('click', async () => {
    logout.disabled = true;
    const out = await call('POST', '/logout', undefined, r.data.csrf_token);
    if (out.ok || out.error.status === 401) {
      location.assign('/login');
      return;
    }
    logout.disabled = false;
    setFormError(out.error.message);
  });
}

wireToggles();
const page = document.body.dataset.page ?? '';
if (page === 'home') void home();
else pages[page]?.(document.querySelector<HTMLFormElement>('#form'));
