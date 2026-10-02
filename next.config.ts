import type { NextConfig } from 'next';

// The site has no server logic: the HubSpot loader, the cookie notice and the
// privacy page all run in the browser. A static export is served by the Caddy
// `site` container (deploy/site/), so there is no Node or Workers runtime to
// operate.
const nextConfig: NextConfig = {
  output: 'export',
};

export default nextConfig;
