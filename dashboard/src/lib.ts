// Shared by the signed-out pages (main.ts) and the dashboard (dashboard.ts).
// Routes, bodies and error codes: docs/spec/accounts.md, "Dashboard API".

export type ApiError = { status: number; code: string; message: string; param?: string };
export type Result<T> = { ok: true; status: number; data: T } | { ok: false; error: ApiError };

export const API = '/dashboard/v1';
export const MIN_PASSWORD = 12;
export const GENERIC = "Something went wrong on our side. Try again in a minute.";

// Same wording as compute-api's own 429 message (internal/dashboard).
export function humanWait(secs: number): string {
  if (secs < 60) return `${secs} seconds`;
  if (secs < 120) return 'a minute';
  return `${Math.ceil(secs / 60)} minutes`;
}

export async function call<T>(method: string, path: string, body?: unknown, csrf?: string): Promise<Result<T>> {
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
    const retry = Number(res.headers.get('Retry-After'));
    if (res.status === 429 && retry > 0 && !/second|minute|hour/.test(message)) {
      message += ` Try again in ${humanWait(retry)}.`;
    }
    return { ok: false, error: { status: res.status, code: e.code, message, param: e.param || undefined } };
  }
  return { ok: false, error: { status: res.status, code: 'unexpected', message: GENERIC } };
}

export const $ = <T extends HTMLElement>(id: string) => document.getElementById(id) as T | null;
export const input = (id: string) => $<HTMLInputElement>(id);

export function setFieldError(name: string, message: string) {
  const el = input(name);
  const err = $(`${name}-error`);
  if (!el || !err) return false;
  el.setAttribute('aria-invalid', 'true');
  err.textContent = message;
  err.hidden = false;
  return true;
}

export function clearFieldError(name: string) {
  input(name)?.removeAttribute('aria-invalid');
  const err = $(`${name}-error`);
  if (err) {
    err.textContent = '';
    err.hidden = true;
  }
}

export function setFormError(message: string, link?: { href: string; text: string }) {
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

export function clearErrors(form: HTMLFormElement) {
  const box = $('form-error');
  if (box) box.hidden = true;
  for (const el of Array.from(form.elements)) {
    if (el instanceof HTMLInputElement && el.id) clearFieldError(el.id);
  }
}

// A field named by `param` gets the message under it; anything else goes in
// the box at the top of the card (UI-STANDARD, "Errors").
export function showApiError(e: ApiError, link?: { href: string; text: string }) {
  if (e.param && setFieldError(e.param, e.message)) {
    input(e.param)?.focus();
    return;
  }
  setFormError(e.message, link);
}

export function focusFirstInvalid(form: HTMLFormElement) {
  form.querySelector<HTMLInputElement>('input[aria-invalid="true"]')?.focus();
}

export function busy(form: HTMLFormElement, on: boolean) {
  const button = form.querySelector<HTMLButtonElement>('button[type="submit"]');
  if (!button) return;
  button.disabled = on;
  button.setAttribute('aria-busy', String(on));
}

export function checkNewPassword(): boolean {
  const pw = input('password')?.value ?? '';
  if (pw.length < MIN_PASSWORD) {
    setFieldError('password', `Use a password of at least ${MIN_PASSWORD} characters.`);
    return false;
  }
  return true;
}

export const MISMATCH = "Passwords don't match.";

export function checkConfirm(): boolean {
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
export function wireConfirm() {
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

export function wireToggles() {
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

export function wireClearOnInput(form: HTMLFormElement) {
  for (const el of Array.from(form.querySelectorAll<HTMLInputElement>('input'))) {
    if (el.id === 'password_confirm') continue;
    el.addEventListener('input', () => {
      if (el.getAttribute('aria-invalid') === 'true') clearFieldError(el.id);
    });
  }
}

export function onSubmit(form: HTMLFormElement, handler: () => Promise<void>) {
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

