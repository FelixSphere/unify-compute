import Link from 'next/link';
import { BrandLockup, CompanyLine } from './brand';
import { CONSOLE_LOGIN, CONSOLE_REGISTER } from './links';

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
        <a href={CONSOLE_LOGIN}>Log in</a>
        <a href={CONSOLE_REGISTER}>Sign up</a>
        <Link href="/privacy" aria-current={here('/privacy')}>
          Privacy
        </Link>
      </nav>
      <CompanyLine />
    </footer>
  );
}
