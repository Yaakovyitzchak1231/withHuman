import { createRootRoute, Navigate, Outlet } from "@tanstack/react-router";
import { ensureRuntimeConfig } from "../app/use-runtime-config";

export const Route = createRootRoute({
  /* Every screen reads the deployment's runtime config; it loads here,
   * once, alongside whatever the destination route loads. */
  loader: ensureRuntimeConfig,
  staleTime: Infinity,
  component: () => <Outlet />,
  notFoundComponent: () => <Navigate to="/queue" replace />,
});
