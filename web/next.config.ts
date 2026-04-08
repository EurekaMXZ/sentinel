import type { NextConfig } from "next";

const apiBase = (process.env.SENTINEL_API_URL ?? "http://127.0.0.1:8080").replace(/\/+$/, "");

const nextConfig: NextConfig = {
  cacheComponents: true,
  partialPrefetching: true,
  allowedDevOrigins: ["127.0.0.1"],
  turbopack: {
    rules: {
      "*.css": {
        loaders: ["@tailwindcss/turbopack"],
        as: "*.css",
      },
    },
  },
  async rewrites() {
    return [
      {
        source: "/v1/:path*",
        destination: `${apiBase}/v1/:path*`,
      },
    ];
  },
};

export default nextConfig;
