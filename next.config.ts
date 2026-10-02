import type { NextConfig } from 'next';

// The site has no server logic: the HubSpot loader, the cookie notice and the
// privacy page all run in the browser. A static export is served by the Caddy
// `site` container (deploy/site/), so there is no Node or Workers runtime to
// operate.
const nextConfig: NextConfig = {
  output: 'export',
  // vinext bakes this into the client bundle and only accepts a client-side
  // navigation payload whose X-Vinext-RSC-Compatibility-Id matches it.
  // Unset, it is a random UUID per build that Caddy cannot echo, and every
  // link click degrades to a full page load. deploy/site/Dockerfile passes
  // the same value to the build and to the Caddyfile.
  deploymentId: process.env.SITE_BUILD_ID || undefined,
};

export default nextConfig;
