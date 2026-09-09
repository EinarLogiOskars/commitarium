#!/bin/sh
set -eu

action="${1:-run}"
compose_override="scripts/compose.codex-worker-smoke.yml"
worker_token="${COMMITARIUM_CODEX_WORKER_TOKEN:-commitarium-local-codex-worker}"

run_codex_login() {
    docker compose --profile real-codex run --rm --no-deps --entrypoint codex \
        codex-worker -c 'cli_auth_credentials_store="file"' login "$@"
}

case "$action" in
login)
    run_codex_login --device-auth
    exit 0
    ;;
status)
    run_codex_login status
    exit 0
    ;;
run)
    ;;
*)
    echo "usage: $0 [login|status|run]" >&2
    exit 2
    ;;
esac

if [ -z "${COMMITARIUM_CODEX_WORKER_PORT:-}" ]; then
    COMMITARIUM_CODEX_WORKER_PORT=$(python3 -c 'import socket; sock=socket.socket(); sock.bind(("127.0.0.1", 0)); print(sock.getsockname()[1]); sock.close()')
    export COMMITARIUM_CODEX_WORKER_PORT
fi

base_url="http://127.0.0.1:$COMMITARIUM_CODEX_WORKER_PORT/internal/v1"
session_id="ses_smoke_$$"
attempt_id="att_smoke_$$"
attempt_url="$base_url/sessions/$session_id/attempts/$attempt_id"
events_file=$(mktemp "${TMPDIR:-/tmp}/commitarium-codex-events.XXXXXX")

cleanup() {
    docker compose -f compose.yml -f "$compose_override" --profile real-codex \
        stop codex-worker >/dev/null 2>&1 || true
    rm -f "$events_file"
}
trap cleanup EXIT INT TERM

docker compose -f compose.yml -f "$compose_override" --profile real-codex \
    up --build --detach --no-deps codex-worker

attempts=0
until curl --fail --silent "$base_url/health" >/dev/null; do
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 120 ]; then
        echo "Codex worker did not become healthy" >&2
        exit 1
    fi
    sleep 0.25
done

launch_body=$(python3 -c '
import json, os
print(json.dumps({
    "mode": "start",
    "assignment": {
        "agent_profile_id": os.getenv("COMMITARIUM_CODEX_PROFILE_ID", "profile_local_codex"),
        "project_id": os.getenv("COMMITARIUM_CODEX_PROJECT_ID", "prj_commitarium"),
        "feature_id": os.getenv("COMMITARIUM_CODEX_FEATURE_ID", "fea_smoke"),
        "role": os.getenv("COMMITARIUM_CODEX_ROLE", "coder"),
        "workspace_id": os.getenv("COMMITARIUM_CODEX_WORKSPACE_ID", "workspace_smoke"),
    },
    "instructions": "Use a repository command to read README.md without modifying anything. Then reply with the exact marker COMMITARIUM_REAL_CODEX_SMOKE_OK followed by one sentence explaining what Commitarium does.",
}))
')

launch_json=$(curl --fail --silent --request PUT "$attempt_url" \
    --header "Authorization: Bearer $worker_token" \
    --header 'Content-Type: application/json' \
    --header "Idempotency-Key: launch-$attempt_id" \
    --data "$launch_body")

printf 'Started real Codex attempt:\n%s\n\nObservable activity:\n' "$launch_json"
curl --fail --no-buffer --silent \
    --header "Authorization: Bearer $worker_token" \
    "$attempt_url/events/stream" | tee "$events_file"

printf '\nFinal attempt:\n'
final_json=$(curl --fail --silent \
    --header "Authorization: Bearer $worker_token" \
    "$attempt_url")
printf '%s\n' "$final_json"

python3 -c '
import json, pathlib, sys

events = pathlib.Path(sys.argv[1]).read_text()
attempt = json.loads(sys.argv[2])
result = attempt.get("result") or {}

if "Codex finished a command with exit code 0." not in events:
    raise SystemExit("real Codex smoke failed: no successful repository command was observed")
if attempt.get("state") != "terminal":
    raise SystemExit("real Codex smoke failed: attempt did not become terminal")
if result.get("outcome") != "completed" or result.get("disposition") != "succeeded":
    raise SystemExit("real Codex smoke failed: attempt did not complete successfully")
if "COMMITARIUM_REAL_CODEX_SMOKE_OK" not in result.get("summary", ""):
    raise SystemExit("real Codex smoke failed: final response omitted the verification marker")

print("Real Codex worker smoke passed.")
' "$events_file" "$final_json"
