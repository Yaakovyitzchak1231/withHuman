import { createFileRoute } from "@tanstack/react-router";
import { Styleguide } from "@/ds/styleguide/styleguide";

export const Route = createFileRoute("/styleguide")({
  component: Styleguide,
});
