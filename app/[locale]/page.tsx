import { notFound } from 'next/navigation';
import { localeOf, type LocaleParams } from '../_pages/locale';
import { HomePage, homeMetadata } from '../_pages/home';

export async function generateMetadata({ params }: LocaleParams) {
  const locale = await localeOf(params);
  return locale ? homeMetadata(locale) : {};
}

export default async function Page({ params }: LocaleParams) {
  const locale = await localeOf(params);
  if (!locale) notFound();
  return <HomePage locale={locale} />;
}
