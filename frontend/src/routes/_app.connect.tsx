import { createFileRoute } from "@tanstack/react-router";
import { useAccess } from "../app/authorization";
import { ConnectScreen } from "../app/connect";

export const Route = createFileRoute("/_app/connect")({
  component: Connect,
});

function Connect() {
  const { holdsAnywhere } = useAccess();
  return <ConnectScreen canCreate={holdsAnywhere("agent.write")} />;
}
