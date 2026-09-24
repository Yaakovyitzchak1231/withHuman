import { createFileRoute } from "@tanstack/react-router";
import { AccountScreen } from "../app/account";

export const Route = createFileRoute("/_app/settings/account")({
  component: AccountScreen,
});
