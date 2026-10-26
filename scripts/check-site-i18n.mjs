// Checks the marketing site's translations (lib/i18n/messages/*.json) against
// the English copy in app/: every key present and non-empty in every
// language, the same {placeholders}, <tags>, [emphasis] and line breaks, the
// product names left as they are, and no key that nothing uses any more.
//   node scripts/check-site-i18n.mjs          check
//   node scripts/check-site-i18n.mjs --keys   print the English keys as JSON
import { readFileSync, readdirSync } from 'node:fs';
import { LOCALES } from '../lib/i18n/config.ts';

const root = new URL('../', import.meta.url);
const read = (p) => readFileSync(new URL(p, root), 'utf8');

function sources(dir) {
  return readdirSync(new URL(dir, root), { withFileTypes: true, recursive: true })
    .filter((e) => e.isFile() && /\.tsx?$/.test(e.name))
    .map((e) => `${e.parentPath}/${e.name}`);
}

// t('literal') and t("literal") calls, including ones the formatter wrapped.
const keys = new Set();
const call = /\bt\(\s*(?:'((?:\\.|[^'\\])*)'|"((?:\\.|[^"\\])*)")/g;
for (const file of sources('app/')) {
  for (const m of readFileSync(file, 'utf8').matchAll(call)) {
    keys.add((m[1] ?? m[2]).replace(/\\(.)/g, (_, c) => (c === 'n' ? '\n' : c)));
  }
}

// Strings the pages pass to t() from data: template titles, descriptions and
// their first sentences, mount descriptions, and the override labels.
const templates = JSON.parse(read('compute-api/config/templates.json')).templates;
const summary = (d) => d.match(/^.+?\.(?=\s+[A-Z])/)?.[0] ?? d;
for (const tpl of templates) {
  keys.add(tpl.title).add(tpl.description).add(summary(tpl.description));
  if (tpl.unavailable_reason) keys.add(tpl.unavailable_reason);
  for (const m of [...tpl.inputs, ...tpl.outputs]) keys.add(m.description);
}
for (const m of read('app/catalogue.ts').matchAll(/^\s+'?[\w.]+'?: '([^']+)',$/gm)) keys.add(m[1]);
for (const m of read('app/catalogue.ts').match(/TIER_LABEL[^{]*\{([^}]*)\}/)[1].matchAll(/: '([^']+)'/g)) keys.add(m[1]);

if (process.argv.includes('--keys')) {
  console.log(JSON.stringify([...keys].sort(), null, 2));
  process.exit(0);
}

const NAMES = ['Unify Compute', 'Unify API', 'Unify Gateway', 'Unify Data', 'Unify SelfAI', 'Unify Token', 'UnifyAI', 'FelixSphere'];
const marks = (s) =>
  [
    ...(s.match(/\{\w+\}/g) ?? []),
    ...(s.match(/<\/?\w+>/g) ?? []),
    `[${(s.match(/\[/g) ?? []).length}]`,
    `\\n${(s.match(/\n/g) ?? []).length}`,
    ...NAMES.filter((n) => s.includes(n)).map((n) => `${n}×${s.split(n).length - 1}`),
    ...(s.match(/[\w.-]+\.(?:jsonl|json|tar\.gz|py|safetensors|md)\b|\/v1\/[\w/]+|\b[a-z0-9]+(?:_[a-z0-9]+)+\b|[A-Z]{2,}_[A-Z_]+/g) ?? []),
  ].sort();

let failed = 0;
const fail = (msg) => {
  failed++;
  console.log(`  ✗ ${msg}`);
};
for (const { code } of LOCALES.filter((l) => l.code !== 'en')) {
  const table = JSON.parse(read(`lib/i18n/messages/${code}.json`));
  console.log(`${code}: ${Object.keys(table).length} keys`);
  for (const k of keys) {
    const v = table[k];
    if (typeof v !== 'string' || v.trim() === '') {
      fail(`${code} missing: ${JSON.stringify(k)}`);
      continue;
    }
    const want = marks(k).join(' ');
    const got = marks(v).join(' ');
    if (want !== got) fail(`${code} ${JSON.stringify(k)}\n      markup/names differ: want [${want}] got [${got}]`);
  }
  for (const k of Object.keys(table)) if (!keys.has(k)) fail(`${code} unused key: ${JSON.stringify(k)}`);
}
console.log(failed ? `FAIL (${failed})` : `PASS: ${keys.size} keys in ${LOCALES.length - 1} languages`);
process.exit(failed ? 1 : 0);
