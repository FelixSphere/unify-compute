import { SiteShell, siteMetadata } from '../shell';

export const metadata = siteMetadata;

export default function EnglishLayout({ children }: { children: React.ReactNode }) {
  return <SiteShell locale="en">{children}</SiteShell>;
}
