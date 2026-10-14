import { LOCALES, localizeHref, type Locale } from '@/lib/i18n/config';
import type { LanguageLink } from './language-switcher';

// The same page in every language, in the standard's order.
export function languageLinks(path: string, locale: Locale): LanguageLink[] {
  return LOCALES.map((l) => ({ code: l.code, prefix: l.prefix, label: l.label, href: localizeHref(path, l.code), current: l.code === locale }));
}
