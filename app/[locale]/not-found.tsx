import type { Metadata } from 'next';
import Link from 'next/link';
import { SiteFooter } from '../site-footer';
import { SiteHeader } from '../site-header';

export const metadata: Metadata = { title: '404: This page could not be found.' };

// English: one 404.html answers every missing path in every language.
export default function NotFound() {
  return (
    <main id="top">
      <SiteHeader locale="en" path="/" />
      <section className="guide wrap">
        <p className="eyebrow">404</p>
        <h1>This page could not be found.</h1>
        <p className="guide-lead">
          <Link href="/">Go to the Unify Compute home page</Link>
        </p>
      </section>
      <SiteFooter locale="en" path="/" />
    </main>
  );
}
