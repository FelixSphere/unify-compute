// Writes dist/client/sitemap.xml and robots.txt after the vinext export.
// vinext's static export skips generated metadata routes (app/sitemap.ts), and
// a hand-kept XML file would drift from the pages, so this builds it from the
// same sources the pages use: the locale table and compute-api's templates.
// Node >= 22.18 strips the types from config.ts itself.
import { readFileSync, writeFileSync } from 'node:fs';
import { LOCALES, BASE_URL, absoluteUrl, languageAlternates } from '../lib/i18n/config.ts';

const out = new URL('../dist/client/', import.meta.url);
const templates = JSON.parse(readFileSync(new URL('../compute-api/config/templates.json', import.meta.url), 'utf8')).templates;
const paths = ['/', '/start', '/marketplace', ...templates.map((t) => `/marketplace/${t.id}`), '/privacy'];

const esc = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/"/g, '&quot;');
const urls = paths.flatMap((path) => {
  const alternates = Object.entries(languageAlternates(path))
    .map(([lang, href]) => `    <xhtml:link rel="alternate" hreflang="${esc(lang)}" href="${esc(href)}"/>`)
    .join('\n');
  return LOCALES.map((l) => `  <url>\n    <loc>${esc(absoluteUrl(path, l.code))}</loc>\n${alternates}\n  </url>`);
});

writeFileSync(
  new URL('sitemap.xml', out),
  `<?xml version="1.0" encoding="UTF-8"?>\n<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:xhtml="http://www.w3.org/1999/xhtml">\n${urls.join('\n')}\n</urlset>\n`,
);
writeFileSync(new URL('robots.txt', out), `User-agent: *\nAllow: /\n\nSitemap: ${BASE_URL}/sitemap.xml\n`);
console.log(`sitemap.xml: ${urls.length} URLs`);
