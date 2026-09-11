import type { Metadata } from 'next';
import { Geist, Geist_Mono } from 'next/font/google';
import './globals.css';

const geistSans = Geist({
  variable: '--font-geist-sans',
  subsets: ['latin'],
});

const geistMono = Geist_Mono({
  variable: '--font-geist-mono',
  subsets: ['latin'],
});

export const metadata: Metadata = {
  icons: {
    icon: [
      { url: '/favicon.ico?v=2', sizes: '16x16 32x32 48x48', type: 'image/x-icon' },
      { url: '/favicon-32-v2.png', sizes: '32x32', type: 'image/png' },
    ],
    shortcut: '/favicon.ico?v=2',
    apple: [{ url: '/apple-touch-icon.png', sizes: '180x180', type: 'image/png' }],
  },
  title: 'unifyAI — On-demand inference & GPU compute',
  description: 'Compute beyond chat. On-demand GPU compute for batch processing, embeddings, document pipelines, and fine-tuning, with consumption-based billing.',
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en">
      <body
        className={`${geistSans.variable} ${geistMono.variable} antialiased`}
      >
        {children}
      </body>
    </html>
  );
}
