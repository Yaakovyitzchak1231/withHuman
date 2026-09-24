import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { createRouter, RouterProvider } from "@tanstack/react-router";
import { reloadOncePerDeploy, ROUTER_OPTIONS } from "./app/router-setup";
import { routeTree } from "./routeTree.gen";
import "./index.css";

/* The open edition's bundle: core's routes and nothing registered on the
 * extension points. The hosted overlay has a main of its own. */
const router = createRouter({ routeTree, ...ROUTER_OPTIONS });
reloadOncePerDeploy();

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <RouterProvider router={router} />
  </StrictMode>,
);
