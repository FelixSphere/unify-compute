// The app host's translations: UI-STANDARD "Languages" (the same ten on
// every product), English-keyed, in the shape Unify Data's web/i18n.js uses
// so a string travels between the products unchanged. Markup carries
// data-i18n / data-i18n-attr / data-i18n-title; code calls t().
//
// Changing the language reloads the page rather than re-translating a live
// one: the pages render their data with t() as they go, and a re-apply
// would put the static English back under what the script already wrote.

import en from './strings/en';

export type Catalogue = Record<string, string>;
export type Locale = { code: string; name: string; dir: 'ltr' | 'rtl' };

export const LOCALES: Locale[] = [
  { code: 'en', name: 'English', dir: 'ltr' },
  { code: 'zh-Hans', name: '简体中文', dir: 'ltr' },
  { code: 'zh-Hant', name: '繁體中文', dir: 'ltr' },
  { code: 'ja', name: '日本語', dir: 'ltr' },
  { code: 'ko', name: '한국어', dir: 'ltr' },
  { code: 'es', name: 'Español', dir: 'ltr' },
  { code: 'fr', name: 'Français', dir: 'ltr' },
  { code: 'de', name: 'Deutsch', dir: 'ltr' },
  { code: 'pt-BR', name: 'Português (Brasil)', dir: 'ltr' },
  { code: 'ar', name: 'العربية', dir: 'rtl' },
];
const CODES = LOCALES.map((l) => l.code);
const STORAGE = 'uc_locale';

// Only the catalogues that exist are imported; a locale still untranslated
// falls back to English for every key, never to the key name.
const loaders: Record<string, () => Promise<{ default: Catalogue }>> = {
  'zh-Hans': () => import('./strings/zh-Hans'),
  'zh-Hant': () => import('./strings/zh-Hant'),
  ja: () => import('./strings/ja'),
  ko: () => import('./strings/ko'),
  es: () => import('./strings/es'),
  fr: () => import('./strings/fr'),
  de: () => import('./strings/de'),
  'pt-BR': () => import('./strings/pt-BR'),
  ar: () => import('./strings/ar'),
};

let current: Catalogue = en;
let code = 'en';

export const locale = (): Locale => LOCALES[Math.max(0, CODES.indexOf(code))];
export const tag = () => locale().code;

export function t(key: string, vars?: Record<string, string | number>): string {
  const text = current[key] ?? en[key] ?? key;
  return vars ? text.replace(/\{(\w+)\}/g, (whole, name: string) => (name in vars ? String(vars[name]) : whole)) : text;
}

// Exact tag, then the base language (so zh-TW lands on zh-Hant, not
// zh-Hans), then English.
function pick(stored: string | null): string {
  const wanted = [stored, ...(navigator.languages ?? [navigator.language ?? 'en'])].filter((x): x is string => !!x);
  for (const candidate of wanted) {
    const lower = candidate.toLowerCase();
    const exact = CODES.find((c) => c.toLowerCase() === lower);
    if (exact) return exact;
    if (lower === 'zh-tw' || lower === 'zh-hk' || lower === 'zh-mo') return 'zh-Hant';
    const base = lower.split('-')[0];
    const loose = CODES.find((c) => c.split('-')[0].toLowerCase() === base);
    if (loose) return loose;
  }
  return 'en';
}

export function apply(root: ParentNode = document) {
  for (const node of root.querySelectorAll<HTMLElement>('[data-i18n]')) node.innerHTML = t(node.dataset.i18n!);
  for (const node of root.querySelectorAll<HTMLElement>('[data-i18n-attr]')) {
    for (const pair of node.dataset.i18nAttr!.split(',')) {
      const [attribute, key] = pair.split(':');
      node.setAttribute(attribute.trim(), t(key.trim()));
    }
  }
  const titled = document.querySelector<HTMLElement>('[data-i18n-title]');
  if (titled) document.title = t(titled.dataset.i18nTitle!);
}

export function picker(target: HTMLElement | null) {
  if (!target) return;
  const select = document.createElement('select');
  select.className = 'locale-picker';
  select.id = 'locale-picker';
  select.setAttribute('aria-label', t('nav.language'));
  for (const l of LOCALES) {
    const o = document.createElement('option');
    o.value = l.code;
    o.textContent = l.name;
    o.selected = l.code === code;
    select.append(o);
  }
  select.addEventListener('change', () => {
    try {
      localStorage.setItem(STORAGE, select.value);
    } catch {}
    location.reload();
  });
  target.replaceChildren(select);
}

export async function start() {
  let stored: string | null = null;
  try {
    stored = localStorage.getItem(STORAGE);
  } catch {}
  code = pick(stored);
  const load = loaders[code];
  if (load) {
    try {
      current = (await load()).default;
    } catch {
      current = en;
    }
  }
  const root = document.documentElement;
  root.lang = locale().code;
  root.dir = locale().dir;
  apply();
  picker(document.getElementById('locale'));
}
