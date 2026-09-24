#!/bin/sh
# Run a build command with optional, process-scoped access to the private AAP
# module. No token is written into a Git URL, file, or persistent configuration.
set -eu
export GOPRIVATE=github.com/agentapprovalprotocol/agentapprovalprotocol
if [ -f /run/secrets/github_token ]; then
  AAP_MODULE_TOKEN=$(cat /run/secrets/github_token)
  export AAP_MODULE_TOKEN
fi
if [ -n "${AAP_MODULE_TOKEN:-}" ]; then
  export GIT_CONFIG_COUNT=1
  export GIT_CONFIG_KEY_0=credential.https://github.com/agentapprovalprotocol.helper
  export GIT_CONFIG_VALUE_0='!f() { if [ "$1" = get ]; then printf "username=x-access-token\npassword=%s\n" "$AAP_MODULE_TOKEN"; fi; }; f'
fi
exec "$@"
