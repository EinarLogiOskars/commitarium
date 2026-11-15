#!/bin/sh
# Checks, on a server, that a deployed app reaches none of Commitarium's own
# services, nor the cloud's metadata service (PUBLISHING_PLAN.md, D4). It
# probes from a throwaway container on each deployment's network, which is
# where the app itself sits. It also checks that Commitarium's own network,
# where agents work, gets nothing from the coordinator without its token
# (COORDINATOR_ACCESS_PLAN.md). Run it as root:
#
#   sudo sh scripts/check-isolation.sh
set -eu

PROBE_IMAGE="busybox:1.36"
DATA_DIR="${COMMITARIUM_DATA_DIR:-/var/lib/commitarium}"
failed=0

say() { printf '%s\n' "$*"; }
# The address a container on $1 reaches the host by.
gateway() { docker network inspect "$1" -f '{{(index .IPAM.Config 0).Gateway}}'; }
# Every address a container has, on whichever networks.
addresses() { docker inspect "$1" -f '{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}' 2>/dev/null || true; }

host_port=$(sed -n 's/.*"port": *\([0-9]*\).*/\1/p' "$DATA_DIR/service.json" | head -1)
public=$(ip route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p')

# An agent's container sits on the stack's network and reaches the
# coordinator by name; with no token it must be refused.
say "From commitarium_default:"
answer=$(docker run --rm --network commitarium_default "$PROBE_IMAGE" sh -c \
  'wget -S -q -O /dev/null http://coordinator:8080/api/v1/projects 2>&1 | sed -n "s/.*HTTP\/[0-9.]* \([0-9][0-9][0-9]\).*/\1/p" | head -1')
if [ "$answer" = "401" ]; then
  say "  The coordinator refuses a call with no token."
else
  failed=1
  say "  OPEN   coordinator:8080 answered ${answer:-nothing} without a token, not 401"
fi

networks=$(docker network ls --format '{{.Name}}' | grep -- '-live\.' || true)
if [ -z "$networks" ]; then
  say "No deployment runs, so there is nothing to check. Deploy an app first."
  exit 1
fi

for network in $networks; do
  say "From $network:"
  gw=$(gateway "$network")
  # What must stay closed: Commitarium's ports on the host, its containers,
  # every other app's local port, and the metadata service.
  targets="$gw:8080 $gw:3001 $gw:$host_port 169.254.169.254:80"
  [ -n "$public" ] && targets="$targets $public:8080 $public:3001 $public:$host_port"
  for container in commitarium-coordinator-1 commitarium-forgejo-1; do
    for ip in $(addresses "$container"); do targets="$targets $ip:8080 $ip:3000"; done
  done
  for port in $(ss -ltnH 2>/dev/null | awk '{print $4}' | sed -n 's/^127\.0\.0\.1:\([0-9]*\)$/\1/p'); do
    targets="$targets $gw:$port"
  done
  open=$(docker run --rm --network "$network" "$PROBE_IMAGE" sh -c "
    for t in $targets; do nc -z -w 2 \${t%:*} \${t#*:} 2>/dev/null && echo \$t; done; true")
  if [ -n "$open" ]; then
    failed=1
    for t in $open; do say "  OPEN   $t"; done
  else
    say "  All $(echo $targets | wc -w | tr -d ' ') closed."
  fi
done

if [ "$failed" -ne 0 ]; then
  say "A deployed app can reach something it must not. See the OPEN lines above."
  exit 1
fi
say "Deployed apps reach none of Commitarium's services."
