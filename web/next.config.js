/** @type {import('next').NextConfig} */
const nextConfig = {
  // Produces a self-contained server.js in .next/standalone (used by Dockerfile)
  output: 'standalone',

  // Proxy API calls to the sm-controller/sm-api backend
  async rewrites() {
    const apiBase = process.env.SM_API_URL || 'http://localhost:8080';
    return [
      {
        source: '/api/:path*',
        destination: `${apiBase}/v1/:path*`,
      },
    ];
  },
};

module.exports = nextConfig;
