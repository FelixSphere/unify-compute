import { localeFromPrefix } from '@/lib/i18n/config';

export type LocaleParams = { params: Promise<{ locale: string }> };

export async function localeOf(params: LocaleParams['params']) {
  return localeFromPrefix((await params).locale);
}
