// The UnifyAI language standard (~/unifyai-brand-kit/UI-STANDARD.md,
// "Languages", user decision 2026-10-13, Vietnamese added
// 2026-10-18): the same eleven languages, in this
// order, under these prefixes, on every product. English has no prefix.
export const LOCALES = [
  { code: 'en', prefix: '', label: 'English', dir: 'ltr' },
  { code: 'zh-Hans', prefix: 'zh', label: '简体中文', dir: 'ltr' },
  { code: 'zh-Hant', prefix: 'zh-hant', label: '繁體中文', dir: 'ltr' },
  { code: 'ja', prefix: 'ja', label: '日本語', dir: 'ltr' },
  { code: 'ko', prefix: 'ko', label: '한국어', dir: 'ltr' },
  { code: 'es', prefix: 'es', label: 'Español', dir: 'ltr' },
  { code: 'fr', prefix: 'fr', label: 'Français', dir: 'ltr' },
  { code: 'de', prefix: 'de', label: 'Deutsch', dir: 'ltr' },
  { code: 'pt-BR', prefix: 'pt-br', label: 'Português (Brasil)', dir: 'ltr' },
  { code: 'vi', prefix: 'vi', label: 'Tiếng Việt', dir: 'ltr' },
  { code: 'ar', prefix: 'ar', label: 'العربية', dir: 'rtl' },
] as const;

export type Locale = (typeof LOCALES)[number]['code'];
export type LocaleConfig = (typeof LOCALES)[number];

export const DEFAULT_LOCALE: Locale = 'en';

// Same name and values as felixsphere.com and Unify Gateway, so one choice
// carries across products. It only remembers the choice: nothing redirects
// on it, or on Accept-Language (the standard forbids that).
export const LOCALE_COOKIE = 'fs_lang';

export const BASE_URL = 'https://compute.unifyai.us';

export const PREFIXED = LOCALES.filter((l) => l.prefix !== '');

export function localeConfig(locale: Locale): LocaleConfig {
  return LOCALES.find((l) => l.code === locale) ?? LOCALES[0];
}

export function localeFromPrefix(prefix: string): Locale | undefined {
  return PREFIXED.find((l) => l.prefix === prefix)?.code;
}

// Every page on this site exists in every language, so any internal path
// (with an optional #hash) takes the prefix. External and bare-hash links
// pass through.
export function localizeHref(href: string, locale: Locale) {
  if (!href.startsWith('/') || locale === DEFAULT_LOCALE) return href;
  const { prefix } = localeConfig(locale);
  const [path, hash] = href.split('#');
  const tail = hash === undefined ? '' : `#${hash}`;
  return path === '/' ? `/${prefix}${tail}` : `/${prefix}${path}${tail}`;
}

export function absoluteUrl(path: string, locale: Locale) {
  const href = localizeHref(path, locale);
  return href === '/' ? `${BASE_URL}/` : `${BASE_URL}${href}`;
}

export function languageAlternates(path: string) {
  const languages: Record<string, string> = {};
  for (const l of LOCALES) languages[l.code] = absoluteUrl(path, l.code);
  languages['x-default'] = absoluteUrl(path, DEFAULT_LOCALE);
  return languages;
}
