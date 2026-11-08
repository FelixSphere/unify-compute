import type { Metadata } from 'next';
import Link from 'next/link';
import { ArrowLeft } from 'lucide-react';
import { localizeHref, type Locale } from '@/lib/i18n/config';
import { rich } from '@/lib/i18n/rich';
import { getT } from '@/lib/i18n/translate';
import { AccountLink } from '../account-link';
import { SIGN_UP_URL } from '../links';
import { SiteFooter } from '../site-footer';
import { SiteHeader } from '../site-header';
import { pageMetadata } from '../shell';
import {
  findListing,
  gpuLabel,
  imageParts,
  OVERRIDE_LABELS,
  PUBLISHER,
  runExample,
  runtimeLabel,
  summary,
} from '../catalogue';

// Template titles and descriptions come from compute-api/config/templates.json
// in English; their translations are message keys like any other copy, so an
// edited description shows in English until it is translated again.
export function listingMetadata(locale: Locale, id: string): Metadata {
  const listing = findListing(id);
  if (!listing) return {};
  const t = getT(locale);
  return pageMetadata(
    locale,
    `/marketplace/${listing.id}`,
    t('{title} — Unify Compute Marketplace', { title: t(listing.title) }),
    t(summary(listing.description)),
  );
}

export function ListingPage({ locale, id }: { locale: Locale; id: string }) {
  const listing = findListing(id);
  if (!listing) return null;
  const t = getT(locale);
  const href = (p: string) => localizeHref(p, locale);
  const { ref, digest } = imageParts(listing.image);
  const env = Object.entries(listing.env ?? {});
  const path = `/marketplace/${listing.id}`;
  const reason = listing.unsoldSku
    ? t('Its GPU ({sku}) is not available yet.', { sku: listing.unsoldSku })
    : listing.reason
      ? t(listing.reason)
      : '';

  return (
    <main id="top">
      <a className="skip-link" href="#listing">
        {t('Skip to content')}
      </a>
      <SiteHeader locale={locale} path={path} current="/marketplace" />
      <article className="guide listing wrap" id="listing">
        <Link className="back-link" href={href('/marketplace')}>
          <ArrowLeft size={16} className="dir-icon" /> {t('All templates')}
        </Link>
        <p className="eyebrow">
          <code>{listing.id}</code>
        </p>
        <h1>{t(listing.title)}</h1>
        <p className="guide-lead">{t(listing.description)}</p>

        <dl className="listing-facts wide">
          <div>
            <dt>{t('Status')}</dt>
            <dd>{listing.runnable ? t('Available') : `${t('Not yet available.')} ${reason}`.trim()}</dd>
          </div>
          <div>
            <dt>GPU</dt>
            <dd dir="ltr">
              {gpuLabel(listing.gpu)} <code>{listing.sku}</code>
            </dd>
          </div>
          <div>
            <dt>{t('Default runtime')}</dt>
            <dd>{t('{runtime} maximum', { runtime: runtimeLabel(listing.max_runtime_seconds) })}</dd>
          </div>
          <div>
            <dt>{t('Publisher')}</dt>
            <dd>{PUBLISHER}</dd>
          </div>
        </dl>

        <h2>{t('What it reads and writes')}</h2>
        <table className="listing-table">
          <tbody>
            {listing.inputs.map((m) => (
              <tr key={m.mount}>
                <th scope="row">{rich(t('Input {mount}'), {}, { mount: <code>{m.mount}</code> })}</th>
                <td>{t(m.description)}</td>
              </tr>
            ))}
            {listing.outputs.map((m) => (
              <tr key={m.mount}>
                <th scope="row">{rich(t('Output {mount}'), {}, { mount: <code>{m.mount}</code> })}</th>
                <td>{t(m.description)}</td>
              </tr>
            ))}
          </tbody>
        </table>

        <h2>{t('What you can change')}</h2>
        <p>
          {rich(
            t(
              'A submit may set: {fields}. The image and the declared inputs and outputs are fixed: a different image is a different template.',
            ),
            {},
            {
              fields: listing.overridable.map((f, i) => (
                <span key={f}>
                  {i > 0 && t(', ')}
                  {t(OVERRIDE_LABELS[f] ?? f)} (<code>{f}</code>)
                </span>
              )),
            },
          )}
        </p>
        {env.length > 0 && (
          <table className="listing-table">
            <caption>{t('Environment defaults')}</caption>
            <tbody>
              {env.map(([k, v]) => (
                <tr key={k}>
                  <th scope="row">
                    <code>{k}</code>
                  </th>
                  <td>
                    <code>{v === '' ? '""' : v}</code>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}

        <h2>{t('Run it')}</h2>
        <p>
          {rich(
            t(
              'With a key in {var} (see the <a>quick start</a>), give each input an {scheme} URL, public or presigned, that ends in the file name above:',
            ),
            { a: (text) => <Link href={href('/start')}>{text}</Link> },
            { var: <code>$UNIFY_KEY</code>, scheme: <code>https://</code> },
          )}
        </p>
        <pre className="code" aria-label={t('Run {id}', { id: listing.id })} dir="ltr">
          <code>{runExample(listing)}</code>
        </pre>
        <p>{t("The job is billed for the GPU time it uses at the SKU's rate, the same as a job you write yourself.")}</p>
        <div className="early-access" role="note">
          <span className="badge">{t('Early access')}</span>
          <p>
            {rich(
              t(
                'GPU job execution is turned on per account while it rolls out. <signup>Create an account</signup>, then ask us to enable it as the <a>quick start</a> describes.',
              ),
              {
                signup: (text) => <AccountLink href={SIGN_UP_URL}>{text}</AccountLink>,
                a: (text) => <Link href={href('/start#account')}>{text}</Link>,
              },
            )}
          </p>
        </div>

        <h2>{t('Where the image comes from')}</h2>
        <table className="listing-table">
          <tbody>
            <tr>
              <th scope="row">{t('Image')}</th>
              <td>
                <code>{ref}</code>
              </td>
            </tr>
            <tr>
              <th scope="row">{t('Digest')}</th>
              <td>
                <code className="digest">{digest}</code>
              </td>
            </tr>
            {listing.image_licence && (
              <tr>
                <th scope="row">{t('Licence')}</th>
                <td dir="ltr">{listing.image_licence}</td>
              </tr>
            )}
            {listing.image_source && (
              <tr>
                <th scope="row">{t('Source')}</th>
                <td dir="ltr">{listing.image_source}</td>
              </tr>
            )}
          </tbody>
        </table>
        <p>
          {t('The digest is what runs. A tag can be re-pushed; a digest cannot, so what was reviewed is what your job pulls.')}
        </p>
      </article>
      <SiteFooter locale={locale} path={path} current="/marketplace" />
    </main>
  );
}
