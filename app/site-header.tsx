import { localizeHref, type Locale } from '@/lib/i18n/config';
import { getT } from '@/lib/i18n/translate';
import { HeaderNav } from './header-nav';
import { languageLinks } from './i18n-links';

// `path` is the page's English path, so the switcher keeps the visitor on the
// same page in the other language.
export function SiteHeader({
  locale,
  path,
  homeHref,
  current,
}: {
  locale: Locale;
  path: string;
  homeHref?: string;
  current?: '/start' | '/marketplace';
}) {
  const t = getT(locale);
  const href = (p: string) => localizeHref(p, locale);
  return (
    <HeaderNav
      homeHref={homeHref ?? href('/')}
      current={current}
      anchors={[
        { href: href('/#workloads'), label: t('Workloads') },
        { href: href('/#how-it-works'), label: t('How it works') },
        { href: href('/#consumption'), label: t('Consumption') },
      ]}
      marketplaceHref={href('/marketplace')}
      startHref={href('/start')}
      labels={{
        home: t('Unify Compute home'),
        nav: t('Main navigation'),
        marketplace: t('Marketplace'),
        quickStart: t('Quick start'),
        logIn: t('Log in'),
        signUp: t('Sign up'),
        openMenu: t('Open menu'),
        closeMenu: t('Close menu'),
        language: t('Language'),
      }}
      languages={languageLinks(path, locale)}
    />
  );
}
