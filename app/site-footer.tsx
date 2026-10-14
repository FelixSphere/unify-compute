import Link from 'next/link';
import { localizeHref, type Locale } from '@/lib/i18n/config';
import { getT } from '@/lib/i18n/translate';
import { BrandLockup, CompanyLine } from './brand';
import { AccountLink } from './account-link';
import { languageLinks } from './i18n-links';
import { LanguageList } from './language-switcher';
import { LOG_IN_URL, SIGN_UP_URL, TERMS_URL } from './links';

export function SiteFooter({
  locale,
  path,
  homeHref,
  current,
}: {
  locale: Locale;
  path: string;
  homeHref?: string;
  current?: '/start' | '/privacy' | '/marketplace';
}) {
  const t = getT(locale);
  const href = (p: string) => localizeHref(p, locale);
  const here = (p: string) => (current === p ? 'page' : undefined);
  return (
    <footer className="wrap">
      <Link href={homeHref ?? href('/')} className="brand" aria-label={t('Unify Compute home')}>
        <BrandLockup decorative />
      </Link>
      <span>{t('Compute beyond chat.')}</span>
      <nav aria-label={t('Footer')} className="footer-links">
        <Link href={href('/marketplace')} aria-current={here('/marketplace')}>
          {t('Marketplace')}
        </Link>
        <Link href={href('/start')} aria-current={here('/start')}>
          {t('Quick start')}
        </Link>
        <AccountLink href={LOG_IN_URL}>{t('Log in')}</AccountLink>
        <AccountLink href={SIGN_UP_URL}>{t('Sign up')}</AccountLink>
        <Link href={href('/privacy')} aria-current={here('/privacy')}>
          {t('Privacy')}
        </Link>
        <a href={TERMS_URL}>{t('Terms')}</a>
      </nav>
      <LanguageList links={languageLinks(path, locale)} label={t('Language')} className="footer-languages" />
      <CompanyLine legal={current === '/privacy'} />
    </footer>
  );
}
