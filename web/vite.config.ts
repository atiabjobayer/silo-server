import { readFileSync, writeFileSync } from "node:fs";
import { brotliCompressSync, constants, gzipSync } from "node:zlib";
import { defineConfig, loadEnv, type Plugin } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "path";
import os from "os";

/// <reference types="vitest" />

const PRECOMPRESS_MIN_BYTES = 1024;

function precompressStaticAssets(): Plugin {
  return {
    name: "precompress-static-assets",
    apply: "build",
    writeBundle(options, bundle) {
      if (!options.dir) return;

      for (const output of Object.values(bundle)) {
        if (!/\.(?:css|js)$/.test(output.fileName)) continue;

        // Read the written file rather than the generateBundle value: later
        // Rollup hooks can still finalize chunk bytes before they reach disk.
        const filePath = path.resolve(options.dir, output.fileName);
        const bytes = readFileSync(filePath);
        if (bytes.byteLength < PRECOMPRESS_MIN_BYTES) continue;

        writeFileSync(
          `${filePath}.br`,
          brotliCompressSync(bytes, {
            params: { [constants.BROTLI_PARAM_QUALITY]: 11 },
          }),
        );
        writeFileSync(`${filePath}.gz`, gzipSync(bytes, { level: 9 }));
      }
    },
  };
}

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  const apiProxyTarget = env.VITE_API_PROXY_TARGET || "http://localhost:8090";
  const hmrClientPort = Number(env.VITE_HMR_CLIENT_PORT || "");
  // Vite's full-screen HMR error overlay is useful locally but is disruptive
  // on a shared dev deployment (e.g. docker-compose.dev.yml on a VPS), where
  // any client-side exception blocks the whole UI behind a red overlay for
  // every viewer until it's dismissed.
  const disableErrorOverlay = ["1", "true", "yes"].includes(
    (env.VITE_DISABLE_ERROR_OVERLAY || "").trim().toLowerCase(),
  );
  // Vite only lets bare IPv4 hosts through unlisted, so a dev server reached by
  // name — the local mDNS name, or a Tailscale MagicDNS name when someone views
  // the dev UI from another device on the tailnet — has to be allowed here.
  // VITE_ALLOWED_HOSTS adds any others (comma-separated).
  const allowedHosts = [
    "silo.local",
    ".ts.net",
    "penguin.atiab.bd",
    os.hostname(),
    ...(env.VITE_ALLOWED_HOSTS || "")
      .split(",")
      .map((host) => host.trim())
      .filter(Boolean),
  ];
  // Remote backends (e.g. the hosted dev server) sit behind vhost-routing
  // proxies that reject a localhost Host header; local backends don't care
  // either way but keeping Host intact preserves existing behavior.
  const apiProxyIsLocal = /^https?:\/\/(localhost|127\.0\.0\.1|\[?::1\]?)(:|\/|$)/.test(
    apiProxyTarget,
  );

  return {
    plugins: [react(), tailwindcss(), precompressStaticAssets()],
    worker: {
      format: "es",
    },
    optimizeDeps: {
      // jassub spawns its own module worker with import.meta.url paths; the
      // dep optimizer rewrites those into .vite/deps where the worker file
      // doesn't exist, so the ASS renderer never initializes in dev.
      exclude: ["jassub"],
      // CJS deps of the excluded package still need prebundling for ESM interop.
      include: ["jassub > throughput", "jassub > rvfc-polyfill"],
    },
    resolve: {
      alias: {
        "@": path.resolve(__dirname, "./src"),
        "@pdfjs": path.resolve(__dirname, "./public/vendor/pdfjs"),
      },
    },
    server: {
      host: "0.0.0.0",
      allowedHosts,
      hmr:
        (Number.isFinite(hmrClientPort) && hmrClientPort > 0) || disableErrorOverlay
          ? {
              ...(Number.isFinite(hmrClientPort) && hmrClientPort > 0
                ? { clientPort: hmrClientPort }
                : {}),
              ...(disableErrorOverlay ? { overlay: false } : {}),
            }
          : undefined,
      proxy: {
        "/api": {
          target: apiProxyTarget,
          changeOrigin: !apiProxyIsLocal,
          secure: true,
          ws: true,
          // Silo's websocket endpoints validate the browser Origin against the
          // request Host or X-Forwarded-Host. With changeOrigin the Host is
          // rewritten to the backend address, so also forward the original
          // client-facing host; otherwise upgrades behind a TLS proxy
          // (e.g. Cloudflare Flexible SSL) are rejected with 403.
          xfwd: true,
          // http-proxy's xfwd only sets X-Forwarded-Host on plain HTTP
          // requests — its websocket upgrade pass never adds it — so every
          // proxied websocket still arrives at the backend without it. Set it
          // by hand from the Host header this dev server actually received.
          configure: (proxy) => {
            proxy.on("proxyReqWs", (proxyReq, req) => {
              const host = req.headers.host;
              if (host) proxyReq.setHeader("x-forwarded-host", host);
            });
          },
        },
      },
    },
    test: {
      environment: "jsdom",
      globals: true,
      setupFiles: ["./src/test-setup.ts"],
    },
  };
});
