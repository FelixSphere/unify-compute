import { t, tag } from '../i18n';
import { $, busy, checkConfirm, checkNewPassword, focusFirstInvalid, input, onSubmit, setFieldError, showApiError, wireConfirm } from '../lib';
import { get, initials, notBuilt, send, setText, shell } from '../shell';

export async function account() {
  const me = await shell();
  if (!me) return;
  setText('me-email', me.user.email);
  setText('me-email-row', me.user.email);
  setText('me-avatar', initials(me.user.email));
  const since = new Intl.DateTimeFormat(tag(), { dateStyle: 'long' }).format(new Date(me.user.created_at * 1000));
  setText('me-sub', t('account.member-since', { date: since }) + (me.account.status !== 'active' ? ` · ${t(`account.status-${me.account.status}`)}` : ''));

  // Copy from Unify Data's profile page ("Other sessions").
  const end = $<HTMLButtonElement>('sessions-end')!;
  const sessionsStatus = $('sessions-status')!;
  async function loadSessions() {
    const r = await get<{ other_sessions: number }>('/sessions');
    if (!r.ok) {
      setText('sessions-count', notBuilt(r.error) ? t('account.other-sessions-will-be-listed') : r.error.message);
      end.hidden = true;
      return;
    }
    const n = r.data.other_sessions;
    setText('sessions-count', n === 0 ? t('account.this-is-your-only-active') : t('account.other-sessions-count', { n }));
    end.hidden = n === 0;
  }
  end.addEventListener('click', async () => {
    sessionsStatus.textContent = '';
    end.disabled = true;
    const r = await send<{ ended: number }>('POST', '/sessions/end-others');
    end.disabled = false;
    if (!r.ok) return showApiError(r.error);
    await loadSessions();
    sessionsStatus.textContent = t('account.signed-out-of-every-other');
    $('sessions-title')?.focus();
  });
  void loadSessions();

  const form = $<HTMLFormElement>('form')!;
  const status = $('password-status')!;
  const wrap = $('password-form-wrap')!;
  const open = $<HTMLButtonElement>('password-open')!;
  const setOpen = (on: boolean) => {
    wrap.hidden = !on;
    open.setAttribute('aria-expanded', String(on));
    if (on) input('current_password')?.focus();
  };
  open.addEventListener('click', () => {
    status.textContent = '';
    form.reset();
    setOpen(!!wrap.hidden);
  });
  $('password-cancel')!.addEventListener('click', () => {
    form.reset();
    setOpen(false);
    open.focus();
  });
  wireConfirm();
  onSubmit(form, async () => {
    status.textContent = '';
    const current = input('current_password')!.value;
    if (!current) setFieldError('current_password', t('account.enter-your-current-password'));
    const pwOk = checkNewPassword();
    const confirmOk = checkConfirm();
    if (!current || !pwOk || !confirmOk) return focusFirstInvalid(form);
    busy(form, true);
    const r = await send('POST', '/password', {
      current_password: current,
      password: input('password')!.value,
      password_confirm: input('password_confirm')!.value,
    });
    if (!r.ok) return showApiError(r.error);
    form.reset();
    setOpen(false);
    status.textContent = t('account.password-changed-every-other-session');
    open.focus();
    void loadSessions();
  });
}
