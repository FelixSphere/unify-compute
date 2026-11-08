import { notFound } from 'next/navigation';
import { PREFIXED, localeFromPrefix } from '@/lib/i18n/config';
import { listings } from '../../../catalogue';
import { ListingPage, listingMetadata } from '../../../_pages/listing';

type Params = { params: Promise<{ locale: string; id: string }> };

export const dynamicParams = false;

export function generateStaticParams() {
  return PREFIXED.flatMap((l) => listings.map((listing) => ({ locale: l.prefix, id: listing.id })));
}

export async function generateMetadata({ params }: Params) {
  const { locale, id } = await params;
  const code = localeFromPrefix(locale);
  return code ? listingMetadata(code, id) : {};
}

export default async function Page({ params }: Params) {
  const { locale, id } = await params;
  const code = localeFromPrefix(locale);
  if (!code) notFound();
  return <ListingPage locale={code} id={id} />;
}
