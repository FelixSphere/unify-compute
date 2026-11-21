import Link from 'next/link';
import { localizeHref, type Locale } from '@/lib/i18n/config';
import { rich } from '@/lib/i18n/rich';
import { getT } from '@/lib/i18n/translate';
import { COMPUTE_API, CONTACT_EMAIL, EARLY_ACCESS_MAILTO, SIGN_UP_URL } from '../links';
import { SiteFooter } from '../site-footer';
import { SiteHeader } from '../site-header';
import { pageMetadata } from '../shell';

export function startMetadata(locale: Locale) {
  const t = getT(locale);
  return pageMetadata(
    locale,
    '/start',
    t('Get started — Unify Compute'),
    t('Browse the Unify Compute catalogue, create an account, and call the Unify Compute API with your key.'),
  );
}

// read -s keeps the key out of the terminal and shell history; the examples
// then expand $UNIFY_KEY instead of carrying the key inline.
const READ_KEY = 'read -rs UNIFY_KEY';
const LIST_SKUS = `curl -s ${COMPUTE_API}/skus`;
const LIST_TEMPLATES = `curl -s ${COMPUTE_API}/templates`;
const LIST_JOBS = `curl -s ${COMPUTE_API}/jobs \\
  -H "Authorization: Bearer $UNIFY_KEY"`;

function Code({ children, label }: { children: string; label: string }) {
  return (
    <pre className="code" aria-label={label} dir="ltr">
      <code>{children}</code>
    </pre>
  );
}

export function StartPage({ locale }: { locale: Locale }) {
  const t = getT(locale);
  return (
    <main id="top">
      <a className="skip-link" href="#guide">
        {t('Skip to content')}
      </a>
      <SiteHeader locale={locale} path="/start" current="/start" />
      <article className="guide wrap" id="guide">
        <p className="eyebrow">{t('Quick start')}</p>
        <h1>{t('Get started with Unify Compute')}</h1>
        <p className="guide-lead">
          {t(
            'Unify Compute is in early access. Anyone can create an account and an API key; GPU job execution is turned on per account while it rolls out.',
          )}
        </p>

        <ol className="guide-steps">
          <li>
            <span className="step-number">01</span>
            <h2>{t('Browse the catalogue')}</h2>
            <p>{t('The GPU SKUs and job templates are public. You can list them now, without a key:')}</p>
            <Code label={t('List SKUs')}>{LIST_SKUS}</Code>
            <Code label={t('List templates')}>{LIST_TEMPLATES}</Code>
            <p>
              {rich(t('Or browse the templates, and the call that runs each one, in the <a>marketplace</a>.'), {
                a: (text) => <Link href={localizeHref('/marketplace', locale)}>{text}</Link>,
              })}
            </p>
          </li>
          <li id="account">
            <span className="step-number">02</span>
            <h2>{t('Create an account')}</h2>
            <ol className="guide-substeps">
              <li>{rich(t('Sign up at {site}.'), {}, { site: <a href={SIGN_UP_URL}>app.compute.unifyai.us</a> })}</li>
              <li>{t('Verify your email address from the link we send you.')}</li>
              <li>{rich(t('Create an API key in the dashboard. Unify Compute keys start with {prefix}.'), {}, { prefix: <code>uck_</code> })}</li>
              <li>
                {rich(
                  t('Ask us to enable job execution for your account: email {email} and tell us what you want to run.'),
                  {},
                  { email: <a href={EARLY_ACCESS_MAILTO}>{CONTACT_EMAIL}</a> },
                )}
              </li>
            </ol>
            <div className="early-access" role="note">
              <span className="badge">{t('Early access')}</span>
              <p>
                {rich(
                  t(
                    'GPU job execution is still rolling out. Until your account is enabled, submitting a job returns {forbidden}; while capacity opens, it may also return {unavailable}.',
                  ),
                  {},
                  { forbidden: <code>403 compute_not_enabled</code>, unavailable: <code>503 upstream_unavailable</code> },
                )}
              </p>
            </div>
          </li>
          <li>
            <span className="step-number">03</span>
            <h2>{t('Call the API with your key')}</h2>
            <p>
              {t(
                'Keep the key in an environment variable rather than typing it into each command. Run this, paste the key, and press Enter; nothing is shown.',
              )}
            </p>
            <Code label={t('Store the key in a shell variable')}>{READ_KEY}</Code>
            <p>{t('List your jobs:')}</p>
            <Code label={t('List your jobs')}>{LIST_JOBS}</Code>
            <p>
              {rich(t('Every endpoint, request and response is described in the <a>OpenAPI document</a>.'), {
                a: (text) => <a href={`${COMPUTE_API}/openapi.json`}>{text}</a>,
              })}
            </p>
          </li>
        </ol>
      </article>
      <SiteFooter locale={locale} path="/start" current="/start" />
    </main>
  );
}
