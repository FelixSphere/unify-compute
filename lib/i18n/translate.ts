import type { Locale } from './config';
import ar from './messages/ar.json';
import de from './messages/de.json';
import es from './messages/es.json';
import fr from './messages/fr.json';
import ja from './messages/ja.json';
import ko from './messages/ko.json';
import ptBR from './messages/pt-BR.json';
import zhHans from './messages/zh-Hans.json';
import zhHant from './messages/zh-Hant.json';

// Messages are keyed by the exact English text, as on felixsphere.com and
// Unify Gateway. Changed English is a new key: the old translation stops
// matching and the page shows English until it is translated, so a page can
// never show a key name, an empty string, or a stale translation.
export const MESSAGES: Record<Exclude<Locale, 'en'>, Record<string, string>> = {
  'zh-Hans': zhHans,
  'zh-Hant': zhHant,
  ja,
  ko,
  es,
  fr,
  de,
  'pt-BR': ptBR,
  ar,
};

export type T = (english: string, vars?: Record<string, string | number>) => string;

export function getT(locale: Locale): T {
  const table = locale === 'en' ? undefined : MESSAGES[locale];
  return (english, vars) => {
    const text = table?.[english] || english;
    return vars ? text.replace(/\{(\w+)\}/g, (m, k: string) => (k in vars ? String(vars[k]) : m)) : text;
  };
}
