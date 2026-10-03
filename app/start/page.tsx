import type { Metadata } from 'next';
import { COMPUTE_API, CONTACT_EMAIL, EARLY_ACCESS_MAILTO } from '../links';
import { SiteFooter } from '../site-footer';
import { SiteHeader } from '../site-header';

export const metadata: Metadata = {
  title: 'Get started — Unify Compute',
  description:
    'Browse the Unify Compute catalogue, request early access, and call the Unify Compute API with your key.',
};

// read -s keeps the key out of the terminal and shell history; the examples
// then expand $UNIFY_KEY instead of carrying the key inline.
const READ_KEY = 'read -rs UNIFY_KEY';
const LIST_SKUS = `curl -s ${COMPUTE_API}/skus`;
const LIST_TEMPLATES = `curl -s ${COMPUTE_API}/templates`;
const LIST_JOBS = `curl -s ${COMPUTE_API}/jobs \\
  -H "Authorization: Bearer $UNIFY_KEY"`;

function Code({ children, label }: { children: string; label: string }) {
  return (
    <pre className="code" aria-label={label}>
      <code>{children}</code>
    </pre>
  );
}

export default function Start() {
  return (
    <main id="top">
      <a className="skip-link" href="#guide">
        Skip to content
      </a>
      <SiteHeader current="/start" />
      <article className="guide wrap" id="guide">
        <p className="eyebrow">Quick start</p>
        <h1>Get started with Unify Compute</h1>
        <p className="guide-lead">
          Unify Compute is in early access. Self-serve sign-up is coming; until
          then, access is by request.
        </p>

        <ol className="guide-steps">
          <li>
            <span className="step-number">01</span>
            <h2>Browse the catalogue</h2>
            <p>
              The GPU SKUs and job templates are public. You can list them now,
              without a key:
            </p>
            <Code label="List SKUs">{LIST_SKUS}</Code>
            <Code label="List templates">{LIST_TEMPLATES}</Code>
          </li>
          <li id="early-access">
            <span className="step-number">02</span>
            <h2>Request early access</h2>
            <div className="early-access" role="note">
              <span className="badge">Early access</span>
              <p>
                Self-serve accounts and log-in are not open yet. Job execution is
                rolling out gradually: submitting a job may return{' '}
                <code>503 upstream_unavailable</code> while it opens.
              </p>
            </div>
            <p>
              Email <a href={EARLY_ACCESS_MAILTO}>{CONTACT_EMAIL}</a> and tell us
              what you want to run. We will reply about access and how to get
              an API key.
            </p>
          </li>
          <li>
            <span className="step-number">03</span>
            <h2>Call the API with your key</h2>
            <p>
              Keep the key in an environment variable rather than typing it into
              each command. Run this, paste the key, and press Enter; nothing is
              shown.
            </p>
            <Code label="Store the key in a shell variable">{READ_KEY}</Code>
            <p>List your jobs:</p>
            <Code label="List your jobs">{LIST_JOBS}</Code>
            <p>
              Every endpoint, request and response is described in the{' '}
              <a href={`${COMPUTE_API}/openapi.json`}>OpenAPI document</a>.
            </p>
          </li>
        </ol>
      </article>
      <SiteFooter current="/start" />
    </main>
  );
}
