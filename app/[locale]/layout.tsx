import { PREFIXED } from '@/lib/i18n/config';
import { SiteShell, siteMetadata } from '../shell';
import { localeOf, type LocaleParams } from '../_pages/locale';

export const metadata = siteMetadata;

export function generateStaticParams() {
  return PREFIXED.map((l) => ({ locale: l.prefix }));
}

// An unknown first segment (/does-not-exist) also lands here; its page calls
// notFound(), and ./not-found.tsx renders inside an English shell. That is
// the export's 404.html, which the static host serves for every missing path.
export default async function LocaleLayout({ children, params }: { children: React.ReactNode } & LocaleParams) {
  return <SiteShell locale={(await localeOf(params)) ?? 'en'}>{children}</SiteShell>;
}
