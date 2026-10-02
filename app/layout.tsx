import type { Metadata } from 'next';
import { IBM_Plex_Mono, Instrument_Sans, Space_Grotesk } from 'next/font/google';
import { CookieNotice } from './cookie-notice';
import './globals.css';

const HUBSPOT_PORTAL_ID = '46127314';

const spaceGrotesk = Space_Grotesk({
  variable: '--font-space-grotesk',
  weight: '700',
  subsets: ['latin'],
});

const instrumentSans = Instrument_Sans({
  variable: '--font-instrument-sans',
  subsets: ['latin'],
});

const plexMono = IBM_Plex_Mono({
  variable: '--font-plex-mono',
  weight: ['400', '500'],
  subsets: ['latin'],
});

const title = 'unifyAI — On-demand inference & GPU compute';
const description =
  'Compute beyond chat. On-demand GPU compute for batch processing, embeddings, document pipelines, and fine-tuning, with consumption-based billing.';
// Browsers keep favicons long after the file changes, so every icon swap
// bumps the version in the file name (or the query, for /favicon.ico and
// /apple-touch-icon.png, which crawlers and iOS also fetch by fixed path).
const ogImage = { url: '/og-image-v3.png', width: 1200, height: 630, alt: 'UnifyAI' };

export const metadata: Metadata = {
  metadataBase: new URL('https://compute.unifyai.us'),
  icons: {
    icon: [
      { url: '/favicon.ico?v=3', sizes: '16x16 32x32', type: 'image/x-icon' },
      { url: '/icon-v3.svg', type: 'image/svg+xml' },
      { url: '/favicon-32-v3.png', sizes: '32x32', type: 'image/png' },
    ],
    shortcut: '/favicon.ico?v=3',
    apple: [{ url: '/apple-touch-icon.png?v=3', sizes: '180x180', type: 'image/png' }],
  },
  title,
  description,
  openGraph: { title, description, url: '/', type: 'website', images: [ogImage] },
  twitter: { card: 'summary_large_image', title, description, images: [ogImage.url] },
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en">
      <body
        className={`${spaceGrotesk.variable} ${instrumentSans.variable} ${plexMono.variable} antialiased`}
      >
        {children}
        <CookieNotice />
        <script
          id="hs-script-loader"
          async
          defer
          src={`https://js.hs-scripts.com/${HUBSPOT_PORTAL_ID}.js`}
        />
      </body>
    </html>
  );
}
