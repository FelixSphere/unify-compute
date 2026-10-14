import type { Metadata } from 'next';
import '@fontsource/space-grotesk/500.css';
import '@fontsource/space-grotesk/700.css';
import '@fontsource/instrument-sans/400.css';
import '@fontsource/instrument-sans/500.css';
import '@fontsource/instrument-sans/600.css';
import '@fontsource/ibm-plex-mono/400.css';
import '@fontsource/ibm-plex-mono/500.css';
import { absoluteUrl, languageAlternates, localeConfig, type Locale } from '@/lib/i18n/config';
import { getT } from '@/lib/i18n/translate';
import { CookieNotice } from './cookie-notice';
import './globals.css';

const HUBSPOT_PORTAL_ID = '46127314';

// Browsers keep favicons long after the file changes, so every icon swap
// bumps the version in the file name (or the query, for /favicon.ico and
// /apple-touch-icon.png, which crawlers and iOS also fetch by fixed path).
const ogImage = { url: '/og-image-v3.png', width: 1200, height: 630, alt: 'UnifyAI' };

export const siteMetadata: Metadata = {
  metadataBase: new URL('https://compute.unifyai.us'),
  icons: {
    icon: [
      { url: '/favicon.ico?v=4', sizes: 'any', type: 'image/x-icon' },
      { url: '/icon-v4.svg', type: 'image/svg+xml' },
      { url: '/favicon-32-v4.png', sizes: '32x32', type: 'image/png' },
    ],
    shortcut: '/favicon.ico?v=4',
    apple: [{ url: '/apple-touch-icon.png?v=3', sizes: '180x180', type: 'image/png' }],
  },
  manifest: '/manifest-v3.webmanifest',
};

// Every public page: its own canonical, all ten languages plus x-default
// (UI-STANDARD "Languages"). `path` is the page's English path.
export function pageMetadata(locale: Locale, path: string, title: string, description: string): Metadata {
  const url = absoluteUrl(path, locale);
  return {
    title,
    description,
    alternates: { canonical: url, languages: languageAlternates(path) },
    openGraph: { title, description, url, type: 'website', locale: locale.replace('-', '_'), images: [ogImage] },
    twitter: { card: 'summary_large_image', title, description, images: [ogImage.url] },
  };
}

// The root layout of both route trees: app/(en) for English at /, and
// app/[locale] for the nine prefixed languages. Two root layouts, because the
// export is static: <html lang dir> has to be right in each written file,
// with no server to set it per request.
export function SiteShell({ locale, children }: { locale: Locale; children: React.ReactNode }) {
  const t = getT(locale);
  return (
    <html lang={locale} dir={localeConfig(locale).dir}>
      <body className="antialiased">
        {children}
        <CookieNotice
          locale={locale}
          labels={{
            title: t('Cookie notice'),
            text: t(
              'We use required, functional, and advertising cookies. Reject to keep only essential cookies. See our <a>Privacy Policy</a>.',
            ),
            reject: t('Reject cookies'),
            accept: t('Accept all'),
          }}
        />
        <script id="hs-script-loader" async defer src={`https://js.hs-scripts.com/${HUBSPOT_PORTAL_ID}.js`} />
      </body>
    </html>
  );
}
