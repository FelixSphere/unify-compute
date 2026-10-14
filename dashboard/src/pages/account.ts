import { $, busy, checkConfirm, checkNewPassword, focusFirstInvalid, input, onSubmit, setFieldError, showApiError, wireConfirm } from '../lib';
import { fill, get, logout, mono, notBuilt, send, setText, shell } from '../shell';

export async function account() {
  const me = await shell();
  if (!me) return;
  setText('me-email', me.user.email);
  fill(
    'me-sub',
    'Account ',
    mono(String(me.account.id)),
    ` · since ${new Intl.DateTimeFormat(undefined, { dateStyle: 'medium' }).format(new Date(me.user.created_at * 1000))}`,
    me.account.status !== 'active' ? ` · ${me.account.status}` : '',
  );
  const out = $<HTMLButtonElement>('logout-main')!;
  out.addEventListener('click', () => void logout(out));

  // Copy from Unify Data's profile page ("Other sessions").
  const end = $<HTMLButtonElement>('sessions-end')!;
  const sessionsStatus = $('sessions-status')!;
  async function loadSessions() {
    const r = await get<{ other_sessions: number }>('/sessions');
    if (!r.ok) {
      setText('sessions-count', notBuilt(r.error) ? 'Other sessions will be listed here once this is live.' : r.error.message);
      end.hidden = true;
      return;
    }
    const n = r.data.other_sessions;
    setText('sessions-count', n === 0 ? 'This is your only active session.' : `Active sessions on other devices or browsers: ${n}.`);
    end.hidden = n === 0;
  }
  end.addEventListener('click', async () => {
    sessionsStatus.textContent = '';
    end.disabled = true;
    const r = await send<{ ended: number }>('POST', '/sessions/end-others');
    end.disabled = false;
    if (!r.ok) return showApiError(r.error);
    await loadSessions();
    sessionsStatus.textContent = 'Signed out of every other session.';
    $('sessions-title')?.focus();
  });
  void loadSessions();

  const form = $<HTMLFormElement>('form')!;
  const status = $('password-status')!;
  wireConfirm();
  onSubmit(form, async () => {
    status.textContent = '';
    const current = input('current_password')!.value;
    if (!current) setFieldError('current_password', 'Enter your current password.');
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
    status.textContent = 'Password changed. Every other session has been logged out.';
    void loadSessions();
  });
}
