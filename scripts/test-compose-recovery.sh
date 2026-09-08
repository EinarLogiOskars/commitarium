#!/bin/sh
set -eu

project_name="commitarium-recovery-test-$$"
COMMITARIUM_COORDINATOR_PORT=$(python3 -c 'import socket; sock=socket.socket(); sock.bind(("127.0.0.1", 0)); print(sock.getsockname()[1]); sock.close()')
export COMMITARIUM_COORDINATOR_PORT
base_url="http://127.0.0.1:$COMMITARIUM_COORDINATOR_PORT"
export COMMITARIUM_SIMULATED_STEP_DELAY="2s"

cleanup() {
    docker compose -p "$project_name" down --volumes --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

json_value() {
    python3 -c 'import json,sys; data=json.load(sys.stdin); value=data'"$1"'; print(value)'
}

wait_for_health() {
    attempts=0
    until curl --fail --silent "$base_url/health" >/dev/null; do
        attempts=$((attempts + 1))
        if [ "$attempts" -ge 120 ]; then
            echo "coordinator did not become healthy" >&2
            return 1
        fi
        sleep 0.25
    done
}

wait_for_run_status() {
    expected="$1"
    attempts=0
    while [ "$attempts" -lt 160 ]; do
        run_json=$(curl --fail --silent "$base_url/api/v1/runs/$run_id")
        status=$(printf '%s' "$run_json" | json_value "['status']")
        if [ "$status" = "$expected" ]; then
            printf '%s' "$run_json"
            return 0
        fi
        attempts=$((attempts + 1))
        sleep 0.25
    done
    echo "run did not reach $expected; last response: $run_json" >&2
    return 1
}

docker compose -p "$project_name" up --build --detach coordinator
wait_for_health

project_json=$(curl --fail --silent --request POST "$base_url/api/v1/projects" \
    --header 'Content-Type: application/json' \
    --data '{"name":"Recovery container test"}')
project_id=$(printf '%s' "$project_json" | json_value "['id']")

feature_json=$(curl --fail --silent --request POST \
    "$base_url/api/v1/projects/$project_id/features" \
    --header 'Content-Type: application/json' \
    --data '{"title":"Recover an interrupted run","description":"Verify restart-safe simulated recovery."}')
feature_id=$(printf '%s' "$feature_json" | json_value "['id']")

start_json=$(curl --fail --silent --request POST \
    "$base_url/api/v1/projects/$project_id/features/$feature_id/runs" \
    --header 'Idempotency-Key: compose-recovery-test' \
    --header 'Content-Length: 0')
run_id=$(printf '%s' "$start_json" | json_value "['id']")

attempts=0
while [ "$attempts" -lt 40 ]; do
    run_json=$(curl --fail --silent "$base_url/api/v1/runs/$run_id")
    active_session_id=$(printf '%s' "$run_json" | python3 -c '
import json,sys
data=json.load(sys.stdin)
active=[item["id"] for item in data["sessions"] if item["status"] == "running"]
print(active[-1] if active else "")
')
    if [ -n "$active_session_id" ]; then
        break
    fi
    attempts=$((attempts + 1))
    sleep 0.05
done
if [ -z "$active_session_id" ]; then
    echo "did not observe an active session before interruption" >&2
    exit 1
fi

docker compose -p "$project_name" stop coordinator
docker compose -p "$project_name" up --detach coordinator
wait_for_health

waiting_json=$(wait_for_run_status waiting_for_user)
recovered_session_id=$(printf '%s' "$waiting_json" | python3 -c '
import json,sys
data=json.load(sys.stdin)
paused=[item for item in data["sessions"] if item["status"] == "paused"]
assert len(paused) == 1, paused
session=paused[0]
assert session["provider_session_id"], session
assert session["recovery_attempt"] >= 1, session
print(session["id"])
')

docker compose -p "$project_name" stop coordinator
docker compose -p "$project_name" up --detach coordinator
wait_for_health

attempts=0
while [ "$attempts" -lt 80 ]; do
    waiting_json=$(wait_for_run_status waiting_for_user)
    recovery_attempt=$(printf '%s' "$waiting_json" | python3 -c '
import json,sys
data=json.load(sys.stdin)
matches=[item for item in data["sessions"] if item["id"] == sys.argv[1]]
print(matches[0]["recovery_attempt"] if matches else 0)
' "$recovered_session_id")
    if [ "$recovery_attempt" -ge 2 ]; then
        break
    fi
    attempts=$((attempts + 1))
    sleep 0.25
done
if [ "$recovery_attempt" -lt 2 ]; then
    echo "repeated startup did not create a second recovery assessment" >&2
    exit 1
fi

events_json=$(curl --fail --silent "$base_url/api/v1/sessions/$recovered_session_id/events")
printf '%s' "$events_json" | python3 -c '
import json,sys
events=json.load(sys.stdin)
assessments=[item for item in events if item["type"] == "recovery_assessment"]
assert len(assessments) >= 2, events
'

curl --fail --silent --request POST \
    "$base_url/api/v1/sessions/$recovered_session_id/commands" \
    --header 'Content-Type: application/json' \
    --header 'Idempotency-Key: approve-compose-recovery' \
    --data '{"type":"continue","message":""}' >/dev/null

completed_json=$(wait_for_run_status succeeded)
printf '%s' "$completed_json" | python3 -c '
import json,sys
data=json.load(sys.stdin)
sessions=data["sessions"]
assert len(sessions) == 6, sessions
assert len({item["id"] for item in sessions}) == 6, sessions
assert all(item["status"] == "completed" for item in sessions), sessions
'

echo "container recovery test passed for run $run_id"
