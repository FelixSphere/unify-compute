import { localeConfig, type Locale } from '@/lib/i18n/config';
import type { T } from '@/lib/i18n/translate';

// The UnifyAI products (~/unifyai-brand-kit/UI-STANDARD.md, "Cross-product
// links", operator rule 2026-11-22). Names, lines and URLs are the standard's
// table, in its order; a change belongs there first. The name is never
// translated, and the line is a literal t() call so check-site-i18n sees it.
export const PRODUCTS: { name: string; url: string; line: (t: T) => string; current?: true }[] = [
  { name: 'Unify API', url: 'https://www.unifyapi.ai', line: (t) => t('One API key for 300+ AI models') },
  { name: 'Unify Gateway', url: 'https://www.gateway.unifyai.us', line: (t) => t('See and control how your company uses AI') },
  { name: 'Unify Compute', url: 'https://compute.unifyai.us', line: (t) => t('Run AI jobs on GPUs on demand'), current: true },
  { name: 'Unify Data', url: 'https://data.unifyai.us', line: (t) => t('Expert training and evaluation data') },
  { name: 'Unify Grid', url: 'https://grid.unifyai.us', line: (t) => t('Lock GPU capacity ahead of time') },
  { name: 'Unify RSI', url: 'https://unifyai.us', line: (t) => t("AI that improves from your team's corrections") },
];

// Every product serves the same eleven locale prefixes (all 66 pages
// answered 200 on 2026-11-22), so a link keeps the visitor's language.
export function productHref(url: string, locale: Locale) {
  return `${url}/${localeConfig(locale).prefix}`;
}
