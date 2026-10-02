#!/bin/sh
# Smoke test predefined roles against a running open-edition server:
# inviting with a role, changing it, what each role may do, and the
# refusal of custom permission policies, which only an edition that
# switches them on offers.
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
curl --silent --fail --cookie "$owner_jar" "$app_api/me" \
  | jq -e 'any(.assignments[]; .is_owner and .built_in_role == "owner") and all(.assignments[]; has("role_id") | not)' >/dev/null
curl --silent --fail "$origin/api/runtime-config" | jq -e '.custom_permissions_enabled == false' >/dev/null
curl --silent --fail --cookie "$owner_jar" "$app_api/roles" \
  | jq -e '[.roles[].key] == ["owner","admin","reviewer","auditor"]' >/dev/null

# Custom permissions are refused, on an invitation and on a team.
policy='{"scope_kind":"organization","resource_ids":[],"permissions":["request.read"]}'
test "$(status_of --cookie "$owner_jar" --header 'Content-Type: application/json' \
  --data "{\"email\":\"custom-$unique@example.com\",\"permission_policies\":[$policy]}" "$app_api/invitations")" = "403"

invitation=$(curl --silent --fail --cookie "$owner_jar" --header 'Content-Type: application/json' \
  --data "{\"email\":\"reviewer-$unique@example.com\",\"role\":\"reviewer\"}" "$app_api/invitations")
printf '%s' "$invitation" | jq -e '(.invitation.grants | length) == 1 and .invitation.grants[0].role == "reviewer"' >/dev/null
url=$(printf '%s' "$invitation" | jq -r .invite_url)
token=${url##*/invite/}
curl --silent --fail --cookie-jar "$member_jar" --header 'Content-Type: application/json' \
  --data "$(jq -nc --arg token "$token" --arg password "$password" '{token:$token,password:$password,display_name:"Smoke Member"}')" \
  "$origin/auth/invitations/accept" >/dev/null
member_id=$(curl --silent --fail --cookie "$member_jar" "$app_api/me" | jq -r .principal.membership_id)
team=$(curl --silent --fail --cookie "$owner_jar" --header 'Content-Type: application/json' \
  --data "{\"name\":\"Payments $unique\"}" "$app_api/teams" | jq -r .team.id)
test "$(status_of --cookie "$owner_jar" --header 'Content-Type: application/json' \
  --header "Idempotency-Key: team-$unique" --data "$policy" "$app_api/teams/$team/permission_policies")" = "403"

# A reviewer decides requests but does not author pipelines; an admin does.
kind="refund-$unique"
curl --silent --fail --cookie "$owner_jar" --header 'Content-Type: application/json' \
  --data "{\"pipeline_key\":\"$kind\",\"name\":\"$kind\",\"blocks\":[]}" "$app_api/request-approval-pipelines" >/dev/null
revision='{"blocks":[]}'
test "$(status_of --cookie "$member_jar" --header 'Content-Type: application/json' --data "$revision" \
  "$app_api/request-approval-pipelines/$kind/revisions")" = "403"
curl --silent --fail --cookie "$owner_jar" --request PUT --header 'Content-Type: application/json' \
  --data '{"role":"admin"}' "$app_api/members/$member_id/role" >/dev/null
curl --silent --fail --cookie "$owner_jar" "$app_api/members" \
  | jq -e --arg id "$member_id" '.members[] | select(.id == $id) | [.assignments[].built_in_role] == ["admin"]' >/dev/null
test "$(status_of --cookie "$member_jar" --header 'Content-Type: application/json' --data "$revision" \
  "$app_api/request-approval-pipelines/$kind/revisions")" = "201"

# Nobody changes their own role, and only an owner grants owner.
test "$(status_of --cookie "$member_jar" --request PUT --header 'Content-Type: application/json' \
  --data '{"role":"owner"}' "$app_api/members/$member_id/role")" = "403"
test "$(status_of --cookie "$member_jar" --header 'Content-Type: application/json' \
  --data "{\"email\":\"owner-$unique@example.com\",\"role\":\"owner\"}" "$app_api/invitations")" = "403"
curl --silent --fail --cookie "$owner_jar" --request PUT --header 'Content-Type: application/json' \
  --data '{"role":"reviewer"}' "$app_api/members/$member_id/role" >/dev/null
test "$(status_of --cookie "$member_jar" --header 'Content-Type: application/json' --data "$revision" \
  "$app_api/request-approval-pipelines/$kind/revisions")" = "403"
echo "predefined role smoke passed"
