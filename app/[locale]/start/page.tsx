import { notFound } from 'next/navigation';
import { localeOf, type LocaleParams } from '../../_pages/locale';
import { StartPage, startMetadata } from '../../_pages/start';

export async function generateMetadata({ params }: LocaleParams) {
  const locale = await localeOf(params);
  return locale ? startMetadata(locale) : {};
}

export default async function Page({ params }: LocaleParams) {
  const locale = await localeOf(params);
  if (!locale) notFound();
  return <StartPage locale={locale} />;
}
