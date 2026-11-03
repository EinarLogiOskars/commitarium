#!/bin/sh
# Tries company sign-in (COMPANY_SIGN_IN_PLAN.md) live on this computer:
# Caddy with the config the host writes, the gate with its arguments, a mock
# OpenID Connect provider signing in "anna" (groups sales, staff), and an app
# that echoes the headers it receives. Usage: try-company-sign-in.sh [groups]
set -eu
S=$(mktemp -d)
trap 'docker rm -f si-caddy si-gate si-app si-login >/dev/null 2>&1; docker network rm sitest >/dev/null 2>&1; rm -rf "$S"' EXIT
GROUPS_ALLOWED=${1-sales}
HOST=$(cd "$(dirname "$0")/../desktop/src-tauri/host" && pwd)
docker rm -f si-caddy si-gate si-app si-login >/dev/null 2>&1 || true
docker network create sitest >/dev/null 2>&1 || true
cat > $S/mock.json <<'J'
{"interactiveLogin":false,"tokenCallbacks":[{"issuerId":"default","tokenExpiry":300,"requestMappings":[{"requestParam":"grant_type","match":"*","claims":{"sub":"anna","email":"anna@example.test","email_verified":true,"preferred_username":"anna","groups":["sales","staff"],"aud":["commitarium"]}}]}]}
J
docker run -d --name si-login --network sitest -e SERVER_PORT=8080 -e JSON_CONFIG="$(cat $S/mock.json)" ghcr.io/navikt/mock-oauth2-server:2.1.10 >/dev/null
(cd $HOST && SIGN_IN_TEST_DIR=$S SIGN_IN_TEST_ISSUER=http://si-login:8080/default SIGN_IN_TEST_GROUPS=$GROUPS_ALLOWED cargo test -q --lib write_a_sign_in_test_setup -- --ignored >/dev/null 2>&1)
# Locally: Caddy's own certificates instead of Let's Encrypt.
python3 - "$S/caddy.json" <<'P'
import json,sys
p=sys.argv[1]; c=json.load(open(p))
c["admin"]={"listen":"localhost:2019"}
c.setdefault("apps",{})["tls"]={"automation":{"policies":[{"issuers":[{"module":"internal"}]}]}}
json.dump(c,open(p,"w"))
P
docker run -d --name si-caddy --network sitest -v $S/caddy.json:/c.json:ro caddy:2.9.1 caddy run --config /c.json >/dev/null
docker run -d --name si-app --network container:si-caddy traefik/whoami:v1.10 --port 3000 >/dev/null
docker run -d --name si-gate --network container:si-caddy -e OAUTH2_PROXY_CLIENT_SECRET=secret -e OAUTH2_PROXY_COOKIE_SECRET=cccccccccccccccccccccccccccccccc quay.io/oauth2-proxy/oauth2-proxy:v7.12.0 $(cat $S/gate-args | tr '\n' ' ') >/dev/null
sleep 6
IP=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' si-caddy)
curl() { docker run --rm --network sitest -v $S:/w curlimages/curl:8.11.1 -sk --resolve todo.example.test:443:$IP --resolve sign-in.example.test:443:$IP "$@"; }
rm -f $S/jar
echo "== not signed in"
curl -o /dev/null -w "%{http_code} -> %{redirect_url}\n" https://todo.example.test/
echo "== sign in and open the app (a forged email header is sent too)"
curl -L -c /w/jar -b /w/jar -H "X-Commitarium-Email: mallory@evil.test" https://todo.example.test/hello | grep -iE "^(GET|X-Commitarium|Host:)" || true
echo "== status with the cookie"
curl -b /w/jar -w "\n%{http_code}\n" https://todo.example.test/ | tail -c 200
