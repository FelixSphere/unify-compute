// The signed-in pages (UI-STANDARD "App shell"). Every route and shape is
// docs/spec/accounts.md, "Dashboard API"; compute-api serves all of them
// since A7d. An older compute-api answers 404 not_found for a route it does
// not register, and one without a job service 501; each section then says
// what will appear there instead of failing the page.

import { account } from './pages/account';
import { billing } from './pages/billing';
import { job } from './pages/job';
import { jobs } from './pages/jobs';
import { keys } from './pages/keys';
import { listings } from './pages/listings';
import { marketplace } from './pages/marketplace';
import { members } from './pages/members';
import { overview } from './pages/overview';
import { usage } from './pages/usage';

export const dashboard: Record<string, () => Promise<void>> = { overview, keys, jobs, job, usage, billing, account, marketplace, listings, members };
