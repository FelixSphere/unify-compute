// Placeholders. Unify Compute is getting its own accounts (the decision that
// supersedes ADR-0001 is being written), and its sign-up and log-in pages do
// not exist yet. Until they do, both point at the early-access section of the
// quick start, which says how to get access today. Change them here only: the
// header, the mobile menu, the hero and the footer all read these, and
// deploy/site/smoke.sh and browser-check.mjs assert the same values.
export const SIGN_UP_URL = '/start#early-access';
export const LOG_IN_URL = '/start#early-access';

// unifyai.us has no mailbox yet; UI-STANDARD allows this one address until it does.
export const CONTACT_EMAIL = 'contactus@felixsphere.com';
export const EARLY_ACCESS_MAILTO = `mailto:${CONTACT_EMAIL}?subject=${encodeURIComponent('Unify Compute early access')}`;

// FelixSphere LLC contracts with Compute's customers (ADR-0011 D2) and the
// operator chose its existing Terms and Conditions on 2026-10-03. The app
// host's pages (dashboard/vite.config.ts) import this too.
export const TERMS_URL = 'https://felixsphere.com/legal/terms';

export const COMPUTE_API = 'https://api.compute.unifyai.us/compute/v1';
