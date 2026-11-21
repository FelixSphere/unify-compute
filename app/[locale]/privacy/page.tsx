import { notFound } from 'next/navigation';
import { localeOf, type LocaleParams } from '../../_pages/locale';
import { PrivacyPage, privacyMetadata } from '../../_pages/privacy';

export async function generateMetadata({ params }: LocaleParams) {
  const locale = await localeOf(params);
  return locale ? privacyMetadata(locale) : {};
}

export default async function Page({ params }: LocaleParams) {
  const locale = await localeOf(params);
  if (!locale) notFound();
  return <PrivacyPage locale={locale} />;
}
