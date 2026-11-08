import { notFound } from 'next/navigation';
import { localeOf, type LocaleParams } from '../../_pages/locale';
import { MarketplacePage, marketplaceMetadata } from '../../_pages/marketplace';

export async function generateMetadata({ params }: LocaleParams) {
  const locale = await localeOf(params);
  return locale ? marketplaceMetadata(locale) : {};
}

export default async function Page({ params }: LocaleParams) {
  const locale = await localeOf(params);
  if (!locale) notFound();
  return <MarketplacePage locale={locale} />;
}
