#!/bin/sh
# Smoke test explicit permission policies, inheritance, resource scope,
# revisions, retries, and revocation against a running open-edition server.
set -eu

origin=${WITHHUMAN_ORIGIN:-http://localhost:8080}
bootstrap_secret=${WITHHUMAN_BOOTSTRAP_SECRET:-change-me-before-production}
unique=$(date +%s)
app_api="$origin/api/v1"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
owner_jar="$work/owner.jar"
member_jar="$work/member.jar"
password="local-development-password"

status_of() {
  curl --silent --output /dev/null --write-out '%{http_code}' "$@"
}

# --- Small organization -----------------------------------------------------
# The open edition bootstraps once per deployment; when an owner already
# exists (./scripts/smoke.sh ran first) sign in as that owner instead.
until curl --silent --fail "$origin/readyz" >/dev/null; do sleep 1; done
owner_email="owner@example.com"
bootstrap_status=$(status_of --cookie-jar "$owner_jar" --header 'Content-Type: application/json' \
  --data "$(jq --null-input --compact-output --arg secret "$bootstrap_secret" --arg email "$owner_email" --arg password "$password" \
    '{secret: $secret, organization_name: "Acme", organization_slug: "acme", display_name: "Test Owner", email: $email, password: $password}')" \
  "$origin/auth/bootstrap")
if [ "$bootstrap_status" = "409" ]; then
  curl --silent --fail --cookie-jar "$owner_jar" --header 'Content-Type: application/json' \
    --data "$(jq --null-input --compact-output --arg email "$owner_email" --arg password "$password" '{email: $email, password: $password}')" \
    "$origin/auth/login" | jq -e '.status == "complete"' >/dev/null
else
  test "$bootstrap_status" = "201"
fi

# Ownership remains protected, and role identifiers stay private.
test "$(status_of --cookie "$owner_jar" "$app_api/roles")" = "404"
curl --silent --fail --cookie "$owner_jar" "$app_api/me" \
  | jq -e 'any(.assignments[]; .is_owner) and all(.assignments[]; has("role_id") | not)' >/dev/null

invitation=$(curl --silent --fail --cookie "$owner_jar" --header 'Content-Type: application/json' \
  --data "{\"email\":\"reviewer-$unique@example.com\",\"permission_policies\":[{\"scope_kind\":\"organization\",\"resource_ids\":[],\"permissions\":[\"request.read\",\"request.decide\"]}]}" "$app_api/invitations")
url=$(printf '%s' "$invitation" | jq -r .invite_url)
token=${url##*/invite/}
curl --silent --fail --cookie-jar "$member_jar" --header 'Content-Type: application/json' \
  --data "$(jq -nc --arg token "$token" --arg password "$password" '{token:$token,password:$password,display_name:"Smoke Member"}')" \
  "$origin/auth/invitations/accept" >/dev/null
member_id=$(curl --silent --fail --cookie "$member_jar" "$app_api/me" | jq -r .principal.membership_id)
team=$(curl --silent --fail --cookie "$owner_jar" --header 'Content-Type: application/json' \
  --data "{\"name\":\"Payments $unique\"}" "$app_api/teams" | jq -r .team.id)
curl --silent --fail --cookie "$owner_jar" --request PUT --header 'Content-Type: application/json' \
  --data "{\"membership_ids\":[\"$member_id\"]}" "$app_api/teams/$team/members" >/dev/null
for kind in "refund-$unique" "release-$unique"; do
  curl --silent --fail --cookie "$owner_jar" --header 'Content-Type: application/json' \
    --data "{\"slug\":\"$kind\",\"name\":\"$kind\"}" "$app_api/agents" >/dev/null
done
policy=$(jq -nc --arg resource "refund-$unique" '{scope_kind:"agent",resource_ids:[$resource],permissions:["pipeline.read","pipeline.write","agent.read","agent.write"]}')
endpoint="$app_api/teams/$team/permission_policies"
saved=$(curl --silent --fail --cookie "$owner_jar" --header 'Content-Type: application/json' \
  --header "Idempotency-Key: scoped-$unique" --data "$policy" "$endpoint")
policy_id=$(printf '%s' "$saved" | jq -r .id)
curl --silent --fail --cookie "$owner_jar" --header 'Content-Type: application/json' \
  --header "Idempotency-Key: scoped-$unique" --data "$policy" "$endpoint" | jq -e --arg id "$policy_id" '.id == $id and .revision == 1' >/dev/null
curl --silent --fail --cookie "$member_jar" "$app_api/members/$member_id/permission_policies" \
  | jq -e --arg team "$team" 'any(.policies[]; .via_team_id == $team and .can_edit == false)' >/dev/null
revision='{"blocks":[]}'
test "$(status_of --cookie "$member_jar" --header 'Content-Type: application/json' --data "$revision" \
  "$app_api/request-approval-pipelines/agents/refund-$unique/revisions")" = "201"
test "$(status_of --cookie "$member_jar" --header 'Content-Type: application/json' --data "$revision" \
  "$app_api/request-approval-pipelines/agents/release-$unique/revisions")" = "403"
test "$(status_of --cookie "$member_jar" --header 'Content-Type: application/json' --data "$policy" \
  --header "Idempotency-Key: forbidden-$unique" "$endpoint")" = "403"
updated=$(printf '%s' "$policy" | jq -c --arg second "release-$unique" '. + {revision:1} | .resource_ids += [$second]')
curl --silent --fail --cookie "$owner_jar" --request PUT --header 'Content-Type: application/json' \
  --header "Idempotency-Key: update-$unique" --data "$updated" "$endpoint/$policy_id" \
  | jq -e '.revision == 2 and (.resource_ids | length) == 2' >/dev/null
test "$(status_of --cookie "$owner_jar" --request PUT --header 'Content-Type: application/json' \
  --header "Idempotency-Key: stale-$unique" --data "$updated" "$endpoint/$policy_id")" = "409"
for retry in 1 2; do
  test "$(status_of --cookie "$owner_jar" --request DELETE --header "Idempotency-Key: delete-$unique" \
    "$endpoint/$policy_id?revision=2")" = "204"
done
test "$(status_of --cookie "$member_jar" --header 'Content-Type: application/json' --data "$revision" \
  "$app_api/request-approval-pipelines/agents/refund-$unique/revisions")" = "403"
curl --silent --fail --cookie "$owner_jar" "$endpoint" \
  | jq -e --arg id "$policy_id" 'all(.policies[]; .id != $id)' >/dev/null
echo "permission policy smoke passed"
