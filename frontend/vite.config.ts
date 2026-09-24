import { fileURLToPath, URL } from "node:url";
import { defineConfig, loadEnv, type ProxyOptions } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { tanstackRouter } from "@tanstack/router-plugin/vite";

export default defineConfig(({ mode }) => {
  /* WITHHUMAN_BACKEND_ORIGIN, from the environment or a local .env
   * file, points the proxy at a backend on a non-default port (for
   * example a Compose stack started with WITHHUMAN_PORT=18080). */
  const env = loadEnv(mode, fileURLToPath(new URL(".", import.meta.url)), "WITHHUMAN_");
  const backend =
    process.env.WITHHUMAN_BACKEND_ORIGIN ??
    env.WITHHUMAN_BACKEND_ORIGIN ??
    "http://localhost:8080";

  /* The dev server is a stand-in for the backend's own origin, so the
   * proxy must present the backend's origin on mutating requests or the
   * server's same-origin CSRF guard rejects them as forbidden. */
  const backendProxy: ProxyOptions = {
    target: backend,
    configure: (proxy) => {
      proxy.on("proxyReq", (proxyReq) => {
        if (proxyReq.getHeader("origin")) {
          proxyReq.setHeader("origin", backend);
        }
      });
    },
  };

  return {
    plugins: [
      /* Must run before the React plugin: it generates routeTree.gen.ts
       * from src/routes. */
      tanstackRouter({ target: "react", autoCodeSplitting: true }),
      react(),
      tailwindcss(),
    ],
    resolve: {
      alias: {
        "@": fileURLToPath(new URL("./src", import.meta.url)),
      },
    },
    build: {
      outDir: "../webapp/assets",
      emptyOutDir: true,
      sourcemap: true,
    },
    server: {
      /* The desktop launcher hands a free port through PORT when 5174
       * is taken; otherwise the preview tab points at a dead port. */
      port: Number(process.env.PORT) || 5174,
      proxy: {
        "/api": backendProxy,
        "/auth": backendProxy,
        "/livez": backendProxy,
        "/readyz": backendProxy,
      },
    },
  };
});
