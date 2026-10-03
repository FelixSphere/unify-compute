import type { Metadata } from 'next';
import { COMPUTE_API, CONSOLE_REGISTER, CONSOLE_TOKENS } from '../links';
import { SiteFooter } from '../site-footer';
import { SiteHeader } from '../site-header';

export const metadata: Metadata = {
  title: 'Get started — Unify Compute',
  description:
    'Create a Unify API account, make an API key, and call the Unify Compute API. One key and one balance for Unify API and Unify Compute.',
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
          Unify Compute uses your Unify API account. One API key and one balance
          cover both Unify API and Unify Compute.
        </p>

        <div className="early-access" role="note">
          <span className="badge">Early access</span>
          <p>
            Job execution is rolling out. Accounts, keys, the SKU catalogue,
            templates and listing your jobs work today. Submitting a job may
            return <code>503 upstream_unavailable</code> while GPU
            execution opens gradually.
          </p>
        </div>

        <ol className="guide-steps">
          <li>
            <span className="step-number">01</span>
            <h2>Create a Unify API account</h2>
            <p>
              Sign up at <a href={CONSOLE_REGISTER}>app.unifyapi.ai/register</a>.
              Email verification happens there. If you already use Unify API,
              skip this step.
            </p>
          </li>
          <li>
            <span className="step-number">02</span>
            <h2>Create an API key</h2>
            <p>
              Open <a href={CONSOLE_TOKENS}>API keys in the Unify API console</a>{' '}
              and create a key. It starts with <code>sk-</code>. The same key
              works for Unify API and Unify Compute, and both draw on the same
              balance.
            </p>
          </li>
          <li>
            <span className="step-number">03</span>
            <h2>Call the API</h2>
            <p>
              Keep the key in an environment variable rather than typing it into
              each command. Run this, paste the key, and press Enter; nothing is
              shown.
            </p>
            <Code label="Store the key in a shell variable">{READ_KEY}</Code>
            <p>List the GPU SKUs. This endpoint is public and needs no key:</p>
            <Code label="List SKUs">{LIST_SKUS}</Code>
            <p>List the job templates, also public:</p>
            <Code label="List templates">{LIST_TEMPLATES}</Code>
            <p>List your jobs, authenticated with your key:</p>
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
