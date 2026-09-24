import { createFileRoute } from "@tanstack/react-router";
import { api } from "../api";
import { AddChannelScreen, isChannelKind, type ChannelKind, type AddChannelPage } from "../app/add-channel";

/* The trailing underscore on notifications_ keeps this page out of the
 * notifications route, the way the API key form sits beside its list.
 * Without a kind it is the picker; with one it is that kind's form, or
 * for the kinds nobody types in, how that channel gets set up. */
export const Route = createFileRoute("/_app/settings/notifications_/add")({
  validateSearch: (search: Record<string, unknown>): { kind?: ChannelKind } =>
    isChannelKind(search.kind) ? { kind: search.kind } : {},
  /* A failed load only costs the picker its "Linked" badges, so it is
   * an empty list rather than an error. */
  loader: (): Promise<AddChannelPage> =>
    api.contactMethods().then(
      (response) => ({
        methods: response.contact_methods,
        slackWorkspace: response.slack_workspace,
        discordServer: response.discord_server,
      }),
      () => ({ methods: [], slackWorkspace: null, discordServer: null }),
    ),
  component: AddChannel,
});

function AddChannel() {
  const page = Route.useLoaderData();
  const { kind } = Route.useSearch();
  return <AddChannelScreen page={page} kind={kind} />;
}
