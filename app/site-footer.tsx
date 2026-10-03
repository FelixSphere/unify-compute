import Link from 'next/link';
import { BrandLockup, CompanyLine } from './brand';
import { AccountLink } from './account-link';
import { LOG_IN_URL, SIGN_UP_URL } from './links';

export function SiteFooter({ homeHref = '/', current }: { homeHref?: string; current?: '/start' | '/privacy' }) {
  const here = (href: string) => (current === href ? 'page' : undefined);
  return (
    <footer className="wrap">
      <Link href={homeHref} className="brand" aria-label="Unify Compute home">
        <BrandLockup decorative />
      </Link>
      <span>Compute beyond chat.</span>
      <nav aria-label="Footer" className="footer-links">
        <Link href="/start" aria-current={here('/start')}>
          Quick start
        </Link>
        <AccountLink href={LOG_IN_URL}>Log in</AccountLink>
        <AccountLink href={SIGN_UP_URL}>Sign up</AccountLink>
        <Link href="/privacy" aria-current={here('/privacy')}>
          Privacy
        </Link>
      </nav>
      <CompanyLine />
    </footer>
  );
}
