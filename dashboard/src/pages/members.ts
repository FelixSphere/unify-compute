// /members (spec/accounts.md "Members and roles"). Any role sees the list;
// owners and admins invite, send again, withdraw, remove and rename; only
// owners change roles. The server decides every one of these; the page only
// hides what would be refused. Copy adapted from Unify Data's Team panel.

import { $, busy, clearFieldError, focusFirstInvalid, input, onSubmit, setFieldError, setFormError, showApiError } from '../lib';
import { type Child, fill, get, h, type Me, muted, notBuilt, type Role, send, setText, shell, state, table, when, withReauth } from '../shell';

type Member = {
  user_id: number;
  email: string;
  role: Role;
  status: 'pending' | 'active' | 'disabled';
  created_at: number;
  last_login_at: number | null;
};
type Invitation = {
  id: string;
  email: string;
  role: 'admin' | 'member';
  status: 'pending' | 'expired';
  invited_by_email: string;
  created_at: number;
  expires_at: number;
};

const ROLE: Record<Role, string> = { owner: 'Owner', admin: 'Admin', member: 'Member' };
const withArticle = (r: Role) => (r === 'member' ? 'a member' : `an ${r}`);
const dateOnly = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium' });
const day = (t: number) => dateOnly.format(new Date(t * 1000));

const canManage = (r: Role) => r === 'owner' || r === 'admin';
const canInviteAs = (actor: Role, role: Role) => actor === 'owner' || (actor === 'admin' && role === 'member');
const canRemove = (actor: Role, self: boolean, target: Role) => self || actor === 'owner' || (actor === 'admin' && target === 'member');

function announce(text: string) {
  setText('members-status', text);
}

// The roles table: what each role can do (spec/accounts.md).
function rolesTable() {
  const yes = 'Yes';
  const no = '—';
  const rows: [string, string, string, string][] = [
    ['See balance, usage, jobs and members', yes, yes, yes],
    ['Create API keys; revoke their own', yes, yes, yes],
    ['Revoke any API key', yes, yes, no],
    ['Top up the balance', yes, yes, no],
    ['Invite and remove members, rename the account', yes, 'Members only', no],
    ['Change roles, hand over ownership', yes, no, no],
  ];
  fill(
    'roles-table',
    table('What each role can do', [{ text: 'Can' }, { text: 'Owner' }, { text: 'Admin' }, { text: 'Member' }], rows),
  );
}

export async function members() {
  const me: Me | null = await shell();
  if (!me) return;
  rolesTable();
  // An older compute-api has no roles: everyone was the single owner.
  const myRole: Role = me.user.role ?? 'owner';
  const manage = canManage(myRole);
  let accountName = me.account.name || me.user.email;

  const showName = () => {
    setText('account-name', accountName);
    setText('account-sub', `You are ${withArticle(myRole)} of this account. Everyone here shares its balance, API keys and jobs.`);
  };
  showName();

  const inviteOpen = $<HTMLButtonElement>('invite-open')!;
  const renameOpen = $<HTMLButtonElement>('rename-open')!;
  inviteOpen.hidden = !manage;
  renameOpen.hidden = !manage;
  $('invitations')!.hidden = !manage;
  if (myRole !== 'owner') $('invite-role-admin')!.hidden = true;

  // ------------------------------------------------------------ lists

  async function loadMembers() {
    const r = await get<Member[]>('/members');
    if (!r.ok) {
      fill('members-list', muted(notBuilt(r.error) ? 'Members will be listed here once members are live in the dashboard.' : r.error.message));
      return;
    }
    fill(
      'members-list',
      table(
        'Members',
        [{ text: 'Email' }, { text: 'Role' }, { text: 'Status' }, { text: 'Last logged in' }, { text: 'Joined' }, { text: '' }],
        r.data.map((m) => {
          const self = m.email === me!.user.email;
          const actions = h('span', { class: 'row-actions' });
          if (myRole === 'owner') {
            actions.append(h('button', { type: 'button', class: 'link', 'data-role': String(m.user_id), 'data-email': m.email, 'data-current': m.role, 'aria-label': `Change the role of ${m.email}` }, 'Change role'));
          }
          if (canRemove(myRole, self, m.role)) {
            actions.append(h('button', { type: 'button', class: 'link', 'data-remove': String(m.user_id), 'data-email': m.email, 'data-self': self ? '1' : '', 'aria-label': self ? 'Leave this account' : `Remove ${m.email}` }, self ? 'Leave' : 'Remove'));
          }
          const st = m.status === 'active' ? state('Active', 'success') : m.status === 'pending' ? state('Unconfirmed', 'warning') : state('Disabled', 'muted');
          const who: Child[] = [m.email];
          if (self) who.push(h('span', { class: 'tag' }, 'you'));
          return [h('span', {}, ...who), ROLE[m.role] ?? m.role, st, m.last_login_at ? when(m.last_login_at) : 'Never', day(m.created_at), actions];
        }),
      ),
    );
  }

  async function loadInvitations() {
    if (!manage) return;
    const r = await get<Invitation[]>('/members/invitations');
    if (!r.ok) {
      fill('invitations-list', muted(notBuilt(r.error) ? 'Invitations will be listed here once members are live in the dashboard.' : r.error.message));
      return;
    }
    if (r.data.length === 0) {
      fill('invitations-list', muted('No invitations are waiting.'));
      return;
    }
    fill(
      'invitations-list',
      table(
        'Pending invitations',
        [{ text: 'Email' }, { text: 'Role' }, { text: 'Invited by' }, { text: 'Status' }, { text: '' }],
        r.data.map((i) => {
          const actions = h('span', { class: 'row-actions' });
          if (canInviteAs(myRole, i.role)) {
            actions.append(
              h('button', { type: 'button', class: 'link', 'data-resend': i.id, 'data-email': i.email, 'aria-label': `Send the invitation to ${i.email} again` }, 'Send again'),
              h('button', { type: 'button', class: 'link', 'data-withdraw': i.id, 'data-email': i.email, 'aria-label': `Withdraw the invitation to ${i.email}` }, 'Withdraw'),
            );
          }
          const st = i.status === 'expired' ? state(`Expired ${day(i.expires_at)}`, 'muted') : state(`Expires ${day(i.expires_at)}`, 'live');
          return [i.email, ROLE[i.role], i.invited_by_email || '—', st, actions];
        }),
      ),
    );
  }

  // ------------------------------------------------------------ invite

  const invite = $('invite')!;
  const inviteForm = $<HTMLFormElement>('invite-form')!;
  const emailField = input('invite-email')!;
  inviteOpen.addEventListener('click', () => {
    inviteForm.reset();
    clearFieldError('invite-email');
    invite.hidden = false;
    // One primary per view: the form's button replaces this one.
    inviteOpen.hidden = true;
    emailField.focus();
  });
  const closeInvite = () => {
    invite.hidden = true;
    inviteOpen.hidden = false;
  };
  $('invite-cancel')!.addEventListener('click', () => {
    closeInvite();
    inviteOpen.focus();
  });
  onSubmit(inviteForm, async () => {
    announce('');
    const email = emailField.value.trim();
    if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email)) {
      setFieldError('invite-email', email ? 'Enter an email address like name@example.com.' : 'Enter the email address to invite.');
      return focusFirstInvalid(inviteForm);
    }
    const role = inviteForm.querySelector<HTMLInputElement>('input[name=role]:checked')?.value ?? 'member';
    busy(inviteForm, true);
    const r = await withReauth(() => send<Invitation>('POST', '/members/invitations', { email, role }));
    if (!r) return;
    if (!r.ok) {
      if (r.error.param === 'email') {
        setFieldError('invite-email', r.error.message);
        return emailField.focus();
      }
      return showApiError(r.error);
    }
    closeInvite();
    announce(`Invitation sent to ${r.data.email}. The link expires ${day(r.data.expires_at)}.`);
    await loadInvitations();
    inviteOpen.focus();
  });

  // ------------------------------------------------------------ invitations

  $('invitations-list')!.addEventListener('click', async (e) => {
    const b = (e.target as Element).closest<HTMLButtonElement>('button[data-resend], button[data-withdraw]');
    if (!b) return;
    announce('');
    b.disabled = true;
    const email = b.dataset.email ?? '';
    const r = b.dataset.resend
      ? await withReauth(() => send<Invitation>('POST', `/members/invitations/${encodeURIComponent(b.dataset.resend!)}/resend`))
      : await send('DELETE', `/members/invitations/${encodeURIComponent(b.dataset.withdraw!)}`);
    b.disabled = false;
    if (!r) return;
    if (!r.ok && r.error.status !== 404) return setFormError(r.error.message);
    announce(b.dataset.resend ? `Invitation sent to ${email} again. The earlier link no longer works.` : `Invitation to ${email} withdrawn.`);
    await loadInvitations();
    $('invitations-title')?.focus();
  });

  // ------------------------------------------------------------ rename

  const renameDialog = $<HTMLDialogElement>('rename-dialog')!;
  const renameField = input('rename-name')!;
  renameOpen.addEventListener('click', () => {
    renameField.value = accountName;
    clearFieldError('rename-name');
    renameDialog.showModal();
    renameField.select();
  });
  $<HTMLFormElement>('rename-form')!.addEventListener('submit', async (e) => {
    if (e.submitter?.getAttribute('value') !== 'save') return;
    e.preventDefault();
    const name = renameField.value.trim();
    if (!name) {
      setFieldError('rename-name', 'Give the account a name.');
      return renameField.focus();
    }
    const save = $<HTMLButtonElement>('rename-save')!;
    save.disabled = true;
    const r = await withReauth(() => send<{ account: { name: string } }>('PATCH', '/account', { name }));
    save.disabled = false;
    if (!r) return;
    if (!r.ok) {
      setFieldError('rename-name', r.error.message);
      return renameField.focus();
    }
    renameDialog.close();
    accountName = r.data.account.name;
    showName();
    announce('Account renamed.');
    renameOpen.focus();
  });

  // ------------------------------------------------------------ role

  const roleDialog = $<HTMLDialogElement>('role-dialog')!;
  const roleError = $('role-error')!;
  let roleTarget: { id: string; opener: HTMLElement } | null = null;
  $('members-list')!.addEventListener('click', (e) => {
    const b = (e.target as Element).closest<HTMLButtonElement>('button[data-role]');
    if (!b) return;
    roleTarget = { id: b.dataset.role!, opener: b };
    setText('role-email', b.dataset.email ?? '');
    for (const r of Array.from(roleDialog.querySelectorAll<HTMLInputElement>('input[name=member-role]'))) r.checked = r.value === b.dataset.current;
    roleError.hidden = true;
    roleDialog.showModal();
  });
  $<HTMLFormElement>('role-form')!.addEventListener('submit', async (e) => {
    if (e.submitter?.getAttribute('value') !== 'save' || !roleTarget) return;
    e.preventDefault();
    const role = roleDialog.querySelector<HTMLInputElement>('input[name=member-role]:checked')?.value;
    if (!role) return;
    const save = $<HTMLButtonElement>('role-save')!;
    save.disabled = true;
    const r = await withReauth(() => send<Member>('PATCH', `/members/${encodeURIComponent(roleTarget!.id)}`, { role }));
    save.disabled = false;
    if (!r) return;
    if (!r.ok) {
      roleError.querySelector('p')!.textContent = r.error.message;
      roleError.hidden = false;
      return;
    }
    roleDialog.close();
    // Demoting yourself changes what this page may offer: start again.
    if (r.data.email === me!.user.email) {
      location.reload();
      return;
    }
    announce(`${r.data.email} is now ${withArticle(r.data.role)}.`);
    await loadMembers();
    $('members-title')?.focus();
  });

  // ------------------------------------------------------------ remove

  const removeDialog = $<HTMLDialogElement>('remove-dialog')!;
  const removeError = $('remove-error')!;
  let removeTarget: { id: string; email: string; self: boolean } | null = null;
  $('members-list')!.addEventListener('click', (e) => {
    const b = (e.target as Element).closest<HTMLButtonElement>('button[data-remove]');
    if (!b) return;
    removeTarget = { id: b.dataset.remove!, email: b.dataset.email ?? '', self: !!b.dataset.self };
    setText('remove-title', removeTarget.self ? 'Leave this account?' : 'Remove this member?');
    setText(
      'remove-text',
      removeTarget.self
        ? `You will be logged out and your login closed. API keys you created stay with ${accountName} until someone revokes them.`
        : `${removeTarget.email} is logged out at once and can no longer sign in to ${accountName}. API keys they created keep working until you revoke them on the API keys page.`,
    );
    setText('remove-confirm', removeTarget.self ? 'Leave' : 'Remove');
    removeError.hidden = true;
    removeDialog.showModal();
  });
  $<HTMLFormElement>('remove-form')!.addEventListener('submit', async (e) => {
    if (e.submitter?.getAttribute('value') !== 'remove' || !removeTarget) return;
    e.preventDefault();
    const confirm = $<HTMLButtonElement>('remove-confirm')!;
    confirm.disabled = true;
    const r = await withReauth(() => send('DELETE', `/members/${encodeURIComponent(removeTarget!.id)}`));
    confirm.disabled = false;
    if (!r) return;
    if (!r.ok && r.error.status !== 404) {
      removeError.querySelector('p')!.textContent = r.error.message;
      removeError.hidden = false;
      return;
    }
    removeDialog.close();
    if (removeTarget.self) {
      location.assign('/login');
      return;
    }
    announce(`${removeTarget.email} was removed.`);
    await loadMembers();
    $('members-title')?.focus();
  });

  await Promise.all([loadMembers(), loadInvitations()]);
}
