#!/bin/sh
set -eu

project_name="commitarium-worker-restart-test-$$"
COMMITARIUM_WORKER_TEST_PORT=$(python3 -c 'import socket; sock=socket.socket(); sock.bind(("127.0.0.1", 0)); print(sock.getsockname()[1]); sock.close()')
export COMMITARIUM_WORKER_TEST_PORT
export COMMITARIUM_WORKER_STEP_DELAY="30s"
export COMMITARIUM_SIMULATED_WORKER_TOKEN="container-test-token"
base_url="http://127.0.0.1:$COMMITARIUM_WORKER_TEST_PORT/internal/v1"
compose_override="scripts/compose.worker-test.yml"

cleanup() {
    docker compose -f compose.yml -f "$compose_override" -p "$project_name" \
        down --volumes --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

wait_for_health() {
    attempts=0
    until curl --fail --silent "$base_url/health" >/dev/null; do
        attempts=$((attempts + 1))
        if [ "$attempts" -ge 120 ]; then
            echo "worker did not become healthy" >&2
            return 1
        fi
        sleep 0.25
    done
}

recreate_worker() {
    docker compose -f compose.yml -f "$compose_override" -p "$project_name" \
        rm --force --stop simulated-codex-worker
    docker compose -f compose.yml -f "$compose_override" -p "$project_name" \
        up --detach --no-build simulated-codex-worker
    wait_for_health
}

assert_running_attempt() {
    python3 -c '
import json,sys
attempt=json.load(sys.stdin)
assert attempt["session_id"] == "ses_container", attempt
assert attempt["attempt_id"] == "att_original", attempt
assert attempt["state"] == "running", attempt
assert attempt["provider_session_id"], attempt
assert attempt["latest_event_sequence"] == 0, attempt
print(attempt["provider_session_id"])
'
}

assert_recovered_attempt() {
    expected_provider_session_id="$1"
    python3 -c '
import json,sys
attempt=json.load(sys.stdin)
assert attempt["session_id"] == "ses_container", attempt
assert attempt["attempt_id"] == "att_original", attempt
assert attempt["state"] == "indeterminate", attempt
assert attempt["provider_session_id"] == sys.argv[1], attempt
' "$expected_provider_session_id"
}

attempt_url="$base_url/sessions/ses_container/attempts/att_original"
launch_body='{"mode":"start","assignment":{"agent_profile_id":"profile_container","project_id":"prj_container","feature_id":"fea_container","role":"coder","workspace_id":"workspace_container","configuration_revision":1,"materialization_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"instructions":"Run the deterministic container recovery test."}'

docker compose -f compose.yml -f "$compose_override" -p "$project_name" \
    up --build --detach simulated-codex-worker
wait_for_health

launch_json=$(curl --fail --silent --request PUT "$attempt_url" \
    --header "Authorization: Bearer $COMMITARIUM_SIMULATED_WORKER_TOKEN" \
    --header 'Content-Type: application/json' \
    --header 'Idempotency-Key: launch-container' \
    --data "$launch_body")
provider_session_id=$(printf '%s' "$launch_json" | assert_running_attempt)

recreate_worker

recovered_json=$(curl --fail --silent "$attempt_url" \
    --header "Authorization: Bearer $COMMITARIUM_SIMULATED_WORKER_TOKEN")
printf '%s' "$recovered_json" | assert_recovered_attempt "$provider_session_id"

retry_output=$(curl --silent --request PUT "$attempt_url" \
    --header "Authorization: Bearer $COMMITARIUM_SIMULATED_WORKER_TOKEN" \
    --header 'Content-Type: application/json' \
    --header 'Idempotency-Key: launch-container' \
    --data "$launch_body" \
    --write-out '\n%{http_code}')
retry_status=$(printf '%s\n' "$retry_output" | tail -n 1)
retry_json=$(printf '%s\n' "$retry_output" | sed '$d')
if [ "$retry_status" != "200" ]; then
    echo "exact launch retry returned HTTP $retry_status: $retry_json" >&2
    exit 1
fi
printf '%s' "$retry_json" | assert_recovered_attempt "$provider_session_id"

replacement_output=$(curl --silent --request PUT \
    "$base_url/sessions/ses_container/attempts/att_replacement" \
    --header "Authorization: Bearer $COMMITARIUM_SIMULATED_WORKER_TOKEN" \
    --header 'Content-Type: application/json' \
    --header 'Idempotency-Key: replacement-container' \
    --data "$launch_body" \
    --write-out '\n%{http_code}')
replacement_status=$(printf '%s\n' "$replacement_output" | tail -n 1)
replacement_json=$(printf '%s\n' "$replacement_output" | sed '$d')
if [ "$replacement_status" != "409" ]; then
    echo "replacement attempt returned HTTP $replacement_status: $replacement_json" >&2
    exit 1
fi
printf '%s' "$replacement_json" | python3 -c '
import json,sys
response=json.load(sys.stdin)
assert response["error"]["code"] == "attempt_active", response
'

recreate_worker
recovered_again_json=$(curl --fail --silent "$attempt_url" \
    --header "Authorization: Bearer $COMMITARIUM_SIMULATED_WORKER_TOKEN")
printf '%s' "$recovered_again_json" | assert_recovered_attempt "$provider_session_id"

echo "standalone worker container restart test passed"
