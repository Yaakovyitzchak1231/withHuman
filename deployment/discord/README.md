# Discord application

withHuman reaches reviewers in Discord through one Discord application per
deployment. Unlike Slack, where each workspace install yields its own bot
token, a Discord application has a single bot token issued in the
Developer Portal. The three settings below are therefore deployment
configuration, all or nothing, and no secrets key is involved: an
organization's install records only which server the bot joined.

## Create the application

1. Open the [Discord Developer Portal](https://discord.com/developers/applications)
   and create an application named for your deployment (for example
   "withHuman approvals").
2. Under **OAuth2**, copy the **Client ID** and reset and copy the
   **Client Secret**.
3. Under **OAuth2 > Redirects**, add both callbacks, replacing the origin
   with your `WITHHUMAN_PUBLIC_ORIGIN`. Discord requires an exact match,
   and unlike Slack it accepts plain `http://localhost` origins for local
   development:
   - `https://approvals.example.com/auth/discord/callback` (organization
     install)
   - `https://approvals.example.com/auth/discord/link/callback` (a person
     linking their account)
4. Under **Bot**, reset and copy the **Token**. No privileged gateway
   intents are needed: the bot never connects to the gateway, it only
   sends direct messages and reads single guild members over REST.
5. Under **Installation**, keep the **Guild Install** context. The install
   URL withHuman builds asks for the `bot` scope with no permissions;
   direct messages need none.

## Configure the deployment

```
WITHHUMAN_DISCORD_CLIENT_ID=...
WITHHUMAN_DISCORD_CLIENT_SECRET=...
WITHHUMAN_DISCORD_BOT_TOKEN=...
```

The worker delivers notifications, so it needs egress to `discord.com`
alongside the server.

## What people see

- An organization admin with `connection.manage` connects a server from
  the Enterprise page. Discord asks them to pick a server they manage and
  adds the bot to it. One server connects to at most one organization.
- Each member links their own account from the Notifications page. Discord
  vouches for the account through the `identify` scope, and the link is
  recorded only if the account is a member of the connected server,
  because a bot can only message people it shares a server with.
- Approval prompts arrive as direct messages from the bot with an "Open in
  withHuman" link and never the request arguments. A member whose privacy
  settings block direct messages from server members will not receive
  them; the failure is logged and the rest of their ladder still runs.
- Disconnecting the server removes every member's link with it and makes
  the bot leave.
