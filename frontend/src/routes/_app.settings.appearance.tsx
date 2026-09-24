import { createFileRoute } from "@tanstack/react-router";
import { AppearanceScreen } from "../app/appearance";

export const Route = createFileRoute("/_app/settings/appearance")({
  component: AppearanceScreen,
});
