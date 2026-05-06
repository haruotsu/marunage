import type { NextConfig } from 'next'

const isDev = process.env.NODE_ENV === 'development'

const nextConfig: NextConfig = {
  ...(isDev ? {} : { output: 'export' }),
  distDir: 'out',
  trailingSlash: true,
  images: { unoptimized: true },
  ...(isDev
    ? {
        async rewrites() {
          return [
            {
              source: '/api/:path*',
              destination: 'http://localhost:7777/api/:path*',
            },
            {
              source: '/events',
              destination: 'http://localhost:7777/events',
            },
          ]
        },
      }
    : {}),
}

export default nextConfig
