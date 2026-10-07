#!/bin/sh
# Guided onboarding end to end against a running stack: the human side
# is driven with curl, the machine side with the real withhuman CLI in a
# throwaway HOME that contains a Claude Code configuration and a fake
# `claude` on PATH, then the installed hook is exercised by piping a
# PreToolUse payload into it and deciding the request from the queue.
set -eu

origin=${WITHHUMAN_ORIGIN:-http://localhost:8080}
app_api=$origin/api/v1
bootstrap_secret=${WITHHUMAN_BOOTSTRAP_SECRET:-change-me-before-production}
local_password=${WITHHUMAN_SMOKE_PASSWORD:-local-development-password}
cli=${WITHHUMAN_CLI:-}
workdir=$(mktemp -d)
cookie_jar=$workdir/cookies
trap 'rm -rf "$workdir"' EXIT

if [ -z "$cli" ]; then
  cli=$workdir/withhuman
  go build -o "$cli" ./cmd/withhuman-cli
fi

until curl --silent --fail "$origin/readyz" >/dev/null; do sleep 1; done

# Sign in as the bootstrap owner (created by scripts/smoke.sh or here).
bootstrap_body=$(jq --null-input --compact-output --arg secret "$bootstrap_secret" --arg password "$local_password" '{
  secret: $secret, organization_slug: "acme", organization_name: "Acme",
  display_name: "Test Owner", email: "owner@example.com", password: $password
}')
bootstrap_status=$(curl --silent --output /dev/null --write-out '%{http_code}' --cookie-jar "$cookie_jar" \
  --header 'Content-Type: application/json' --data "$bootstrap_body" "$origin/auth/bootstrap")
test "$bootstrap_status" = "201" || test "$bootstrap_status" = "409"
curl --silent --fail --cookie-jar "$cookie_jar" --header 'Content-Type: application/json' \
  --data "$(jq --null-input --compact-output --arg password "$local_password" '{email:"owner@example.com",password:$password}')" \
  "$origin/auth/login" | jq -e '.status == "complete"' >/dev/null

# A throwaway machine: Claude Code "installed" (a fake binary on PATH and
# a home directory with settings), nothing else.
runtime_home=$workdir/home
mkdir -p "$runtime_home/.claude" "$workdir/bin"
printf '{}\n' > "$runtime_home/.claude/settings.json"
printf '{"mcpServers":{}}\n' > "$runtime_home/.claude.json"
cat > "$workdir/bin/claude" <<'FAKE'
#!/bin/sh
if [ "${1:-}" = "--version" ]; then echo "2.0.0 (Claude Code)"; fi
FAKE
chmod +x "$workdir/bin/claude"
config_dir=$workdir/config

# The page starts a session and hands out the setup code.
begun=$(curl --silent --fail --cookie "$cookie_jar" --header 'Content-Type: application/json' \
  --header "Origin: $origin" --data '{}' "$app_api/onboarding/sessions")
setup_code=$(printf '%s' "$begun" | jq -r .setup_code)
printf '%s' "$begun" | jq -e '.setup_code as $code | .session.status == "awaiting_cli" and (.setup_command | startswith("curl -fsSL")) and (.setup_command | contains("--code " + $code))' >/dev/null

# The CLI runs in the background: it connects, detects, waits for the
# person's choices, discovers, enrolls, installs the hook, and exits once
# the session is installed.
HOME=$runtime_home PATH="$workdir/bin:$PATH" WITHHUMAN_CONFIG_DIR=$config_dir AAP_CONFIG_DIR=$config_dir/aap \
  "$cli" onboard --url "$origin" --code "$setup_code" >"$workdir/setup.log" 2>&1 &
cli_pid=$!

wait_for_status() {
  target=$1
  attempts=0
  while :; do
    status=$(curl --silent --fail --cookie "$cookie_jar" "$app_api/onboarding/session" | jq -r .status)
    [ "$status" = "$target" ] && return 0
    attempts=$((attempts + 1))
    if [ "$attempts" -gt 60 ]; then
      echo "session stuck in $status waiting for $target" >&2
      cat "$workdir/setup.log" >&2
      return 1
    fi
    sleep 1
  done
}

wait_for_status detected
curl --silent --fail --cookie "$cookie_jar" "$app_api/onboarding/session" \
  | jq -e '[.detections[] | select(.runtime == "claude-code")][0].installed == true' >/dev/null
curl --silent --fail --cookie "$cookie_jar" --request PUT --header 'Content-Type: application/json' --header "Origin: $origin" \
  --data '{"runtimes":["claude-code"]}' "$app_api/onboarding/session/agents" | jq -e '.status == "agents_selected"' >/dev/null

wait_for_status tools_discovered
curl --silent --fail --cookie "$cookie_jar" "$app_api/onboarding/session" \
  | jq -e '[.tools["claude-code"][] | select(.name == "Bash")][0].suggested == true' >/dev/null
# The notification steps come before the agents are connected; the
# rules step provisions once they are stamped.
provision_status=$(curl --silent --output /dev/null --write-out '%{http_code}' --cookie "$cookie_jar" --request POST --header "Origin: $origin" "$app_api/onboarding/session/provision")
test "$provision_status" = "400"
curl --silent --fail --cookie "$cookie_jar" --request POST --header "Origin: $origin" "$app_api/onboarding/session/channels" \
  | jq -e '.status == "tools_discovered" and .channels_set_at != null' >/dev/null
provisioned=$(curl --silent --fail --cookie "$cookie_jar" --request POST --header "Origin: $origin" "$app_api/onboarding/session/provision")
printf '%s' "$provisioned" | jq -e '.status == "provisioning" and (.generated.pipelines["claude-code"] | .pipeline_key == "entry" and .branch_key == "claude-code" and .revision >= 1)' >/dev/null
# The rules live in the entry pipeline as one branch per runtime.
curl --silent --fail --cookie "$cookie_jar" "$app_api/request-approval-pipelines/entry" \
  | jq -e '[.blocks[] | select(.id == "claude-code" and .type == "branch")] | length == 1' >/dev/null
path_key=$(printf '%s' "$provisioned" | jq -r .generated.escalation_path_key)
curl --silent --fail --cookie "$cookie_jar" "$app_api/escalation-paths/$path_key" | jq -e 'type == "object"' >/dev/null

wait_for_status installed
wait "$cli_pid" || { cat "$workdir/setup.log" >&2; exit 1; }
session=$(curl --silent --fail --cookie "$cookie_jar" "$app_api/onboarding/session")
agent_slug=$(printf '%s' "$session" | jq -r '.enrollments[0].agent_slug')
test "$agent_slug" = "claude-code"
printf '%s' "$session" | jq -e '.install_results[0].ok == true and .test_commands == {}' >/dev/null
# Leaving the rules step chooses the test call the starting rules hold.
curl --silent --fail --cookie "$cookie_jar" --request POST --header "Origin: $origin" "$app_api/onboarding/session/rules" \
  | jq -e '.status == "installed" and .rules_set_at != null and (.test_commands["claude-code"].command | startswith("claude -p"))' >/dev/null
jq -e '[.hooks.PreToolUse[]?.hooks[]?.command | select(contains("hook claude-code"))] | length == 1' "$runtime_home/.claude/settings.json" >/dev/null
test -f "$config_dir/credentials/claude-code.json"

# The installed hook holds a gated call until the queue decides it.
hook_payload='{"session_id":"smoke","tool_name":"Bash","tool_input":{"command":"echo hello from withHuman"},"tool_use_id":"call-1","cwd":"/tmp"}'
decide_latest() {
  outcome=$1
  attempts=0
  while :; do
    request_id=$(curl --silent --fail --cookie "$cookie_jar" "$app_api/requests?agent_slug=$agent_slug" \
      | jq -r '[.requests[] | select(.status == "pending")][0].id // empty')
    [ -n "$request_id" ] && break
    attempts=$((attempts + 1))
    [ "$attempts" -gt 30 ] && { echo "no pending request appeared" >&2; return 1; }
    sleep 1
  done
  curl --silent --fail --cookie "$cookie_jar" --header 'Content-Type: application/json' --header "Origin: $origin" \
    --header "Idempotency-Key: smoke-$outcome-$request_id" \
    --data "{\"status\":\"$outcome\",\"note\":\"smoke\",\"channel\":\"web\"}" "$app_api/requests/$request_id/decision" >/dev/null
}

printf '%s' "$hook_payload" | HOME=$runtime_home WITHHUMAN_CONFIG_DIR=$config_dir AAP_CONFIG_DIR=$config_dir/aap \
  "$cli" hook claude-code >"$workdir/approve.out" 2>"$workdir/approve.err" &
hook_pid=$!
decide_latest approved
wait "$hook_pid"
test ! -s "$workdir/approve.out"

denied_payload=$(printf '%s' "$hook_payload" | jq --compact-output '.tool_use_id = "call-2"')
printf '%s' "$denied_payload" | HOME=$runtime_home WITHHUMAN_CONFIG_DIR=$config_dir AAP_CONFIG_DIR=$config_dir/aap \
  "$cli" hook claude-code >"$workdir/deny.out" 2>"$workdir/deny.err" &
hook_pid=$!
decide_latest denied
wait "$hook_pid"
jq -e '.hookSpecificOutput.permissionDecision == "deny"' "$workdir/deny.out" >/dev/null

# The person records the request they decided; finishing waits for it.
finish_status=$(curl --silent --output /dev/null --write-out '%{http_code}' --cookie "$cookie_jar" --header 'Content-Type: application/json' --header "Origin: $origin" \
  --data '{"outcome":"finished"}' "$app_api/onboarding/completion")
test "$finish_status" = "409"
curl --silent --fail --cookie "$cookie_jar" --header 'Content-Type: application/json' --header "Origin: $origin" \
  --data "{\"request_id\":\"$request_id\"}" "$app_api/onboarding/session/test" \
  | jq -e --arg id "$request_id" '.status == "tested" and .test_request_id == $id and .tested_at != null' >/dev/null

# A tool the person did not select still goes through the provider: the
# pipeline approves it at once, the hook passes it through, and the call
# is on record with a pipeline decision rather than a person's.
ungated=$(printf '%s' "$hook_payload" | jq --compact-output '.tool_name = "Read" | .tool_input = {file_path: "/tmp/x"} | .tool_use_id = "call-3"')
printf '%s' "$ungated" | HOME=$runtime_home WITHHUMAN_CONFIG_DIR=$config_dir AAP_CONFIG_DIR=$config_dir/aap "$cli" hook claude-code >"$workdir/ungated.out"
test ! -s "$workdir/ungated.out"
curl --silent --fail --cookie "$cookie_jar" "$app_api/requests?agent_slug=$agent_slug" \
  | jq -e '[.requests[] | select(.tool == "Read")] | length == 1 and .[0].status == "approved" and .[0].decision.channel == "pipeline"' >/dev/null

curl --silent --fail --cookie "$cookie_jar" --header 'Content-Type: application/json' --header "Origin: $origin" \
  --data '{"outcome":"finished"}' "$app_api/onboarding/completion" >/dev/null
curl --silent --fail --cookie "$cookie_jar" "$app_api/me" | jq -e '.onboarding.required == false' >/dev/null

# Eject restores the settings file and removes the credential.
HOME=$runtime_home WITHHUMAN_CONFIG_DIR=$config_dir AAP_CONFIG_DIR=$config_dir/aap "$cli" agent eject claude-code --yes >/dev/null
jq -e '(.hooks.PreToolUse // []) | length == 0' "$runtime_home/.claude/settings.json" >/dev/null
test ! -f "$config_dir/credentials/claude-code.json"

echo "onboarding smoke passed"
