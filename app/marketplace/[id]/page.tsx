import type { Metadata } from 'next';
import Link from 'next/link';
import { ArrowLeft } from 'lucide-react';
import { AccountLink } from '../../account-link';
import { SIGN_UP_URL } from '../../links';
import { SiteFooter } from '../../site-footer';
import { SiteHeader } from '../../site-header';
import {
  findListing,
  gpuLabel,
  imageParts,
  listings,
  OVERRIDE_LABELS,
  PUBLISHER,
  runExample,
  runtimeLabel,
  summary,
} from '../catalogue';

type Params = { params: Promise<{ id: string }> };

export const dynamicParams = false;

export function generateStaticParams() {
  return listings.map((l) => ({ id: l.id }));
}

export async function generateMetadata({ params }: Params): Promise<Metadata> {
  const listing = findListing((await params).id);
  if (!listing) return {};
  return { title: `${listing.title} — Unify Compute Marketplace`, description: summary(listing.description) };
}

export default async function ListingPage({ params }: Params) {
  const listing = findListing((await params).id);
  if (!listing) return null;
  const { ref, digest } = imageParts(listing.image);
  const env = Object.entries(listing.env ?? {});

  return (
    <main id="top">
      <a className="skip-link" href="#listing">
        Skip to content
      </a>
      <SiteHeader current="/marketplace" />
      <article className="guide listing wrap" id="listing">
        <Link className="back-link" href="/marketplace">
          <ArrowLeft size={16} /> All templates
        </Link>
        <p className="eyebrow">
          <code>{listing.id}</code>
        </p>
        <h1>{listing.title}</h1>
        <p className="guide-lead">{listing.description}</p>

        <dl className="listing-facts wide">
          <div>
            <dt>Status</dt>
            <dd>{listing.runnable ? 'Available' : `Not yet available. ${listing.reason ?? ''}`}</dd>
          </div>
          <div>
            <dt>GPU</dt>
            <dd>
              {gpuLabel(listing.gpu)} <code>{listing.sku}</code>
            </dd>
          </div>
          <div>
            <dt>Default runtime</dt>
            <dd>{runtimeLabel(listing.max_runtime_seconds)} maximum</dd>
          </div>
          <div>
            <dt>Publisher</dt>
            <dd>{PUBLISHER}</dd>
          </div>
        </dl>

        <h2>What it reads and writes</h2>
        <table className="listing-table">
          <tbody>
            {listing.inputs.map((m) => (
              <tr key={m.mount}>
                <th scope="row">
                  Input <code>{m.mount}</code>
                </th>
                <td>{m.description}</td>
              </tr>
            ))}
            {listing.outputs.map((m) => (
              <tr key={m.mount}>
                <th scope="row">
                  Output <code>{m.mount}</code>
                </th>
                <td>{m.description}</td>
              </tr>
            ))}
          </tbody>
        </table>

        <h2>What you can change</h2>
        <p>
          A submit may set:{' '}
          {listing.overridable.map((f, i) => (
            <span key={f}>
              {i > 0 && ', '}
              {OVERRIDE_LABELS[f] ?? f} (<code>{f}</code>)
            </span>
          ))}
          . The image and the declared inputs and outputs are fixed: a different
          image is a different template.
        </p>
        {env.length > 0 && (
          <table className="listing-table">
            <caption>Environment defaults</caption>
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

        <h2>Run it</h2>
        <p>
          With a key in <code>$UNIFY_KEY</code> (see the{' '}
          <Link href="/start">quick start</Link>), give each input an{' '}
          <code>https://</code> URL, public or presigned, that ends in the file
          name above:
        </p>
        <pre className="code" aria-label={`Run ${listing.id}`}>
          <code>{runExample(listing)}</code>
        </pre>
        <p>
          The job is billed for the GPU time it uses at the SKU&apos;s rate, the
          same as a job you write yourself.
        </p>
        <div className="early-access" role="note">
          <span className="badge">Early access</span>
          <p>
            GPU job execution is turned on per account while it rolls out.{' '}
            <AccountLink href={SIGN_UP_URL}>Create an account</AccountLink>,
            then ask us to enable it as the <Link href="/start#account">quick start</Link> describes.
          </p>
        </div>

        <h2>Where the image comes from</h2>
        <table className="listing-table">
          <tbody>
            <tr>
              <th scope="row">Image</th>
              <td>
                <code>{ref}</code>
              </td>
            </tr>
            <tr>
              <th scope="row">Digest</th>
              <td>
                <code className="digest">{digest}</code>
              </td>
            </tr>
            {listing.image_licence && (
              <tr>
                <th scope="row">Licence</th>
                <td>{listing.image_licence}</td>
              </tr>
            )}
            {listing.image_source && (
              <tr>
                <th scope="row">Source</th>
                <td>{listing.image_source}</td>
              </tr>
            )}
          </tbody>
        </table>
        <p>
          The digest is what runs. A tag can be re-pushed; a digest cannot, so
          what was reviewed is what your job pulls.
        </p>
      </article>
      <SiteFooter current="/marketplace" />
    </main>
  );
}
