#!/bin/sh
# Installs Commitarium's host as a service on a Linux server, or repairs and
# upgrades one. Data is kept. Run with --uninstall to remove the service.
#
#   curl -fsSL https://github.com/EinarLogiOskars/commitarium/releases/latest/download/install-host.sh | sudo sh
set -eu

REPOSITORY="EinarLogiOskars/commitarium"
SERVICE="commitarium-host"
SERVICE_USER="commitarium"
# The same id the containers run as, so they can read what the service writes.
SERVICE_ID=65532
COMMAND="/usr/local/bin/commitarium-host"
UNIT="/etc/systemd/system/${SERVICE}.service"
DATA_DIR="/var/lib/commitarium"
# The service updates itself, so it owns its own program.
BINARY="$DATA_DIR/bin/commitarium-host"
MINIMUM_DISK_GB=20
MINIMUM_MEMORY_GB=2
READY_SECONDS=900

say() { printf '%s\n' "$*"; }
stop() {
  say "Commitarium could not be installed: $*" >&2
  exit 1
}

ask() {
  # Under `curl | sh` stdin is the script, so the answer comes from the terminal.
  [ -r /dev/tty ] || return 1
  printf '%s [Y/n] ' "$1" >/dev/tty
  read -r answer </dev/tty || return 1
  case "$answer" in
    "" | y | Y | yes) return 0 ;;
    *) return 1 ;;
  esac
}

check_machine() {
  [ "$(uname -s)" = "Linux" ] || stop "this script is for Linux servers."
  [ "$(id -u)" -eq 0 ] || stop "run it with sudo."
  command -v systemctl >/dev/null 2>&1 || stop "this server has no systemd."
  command -v curl >/dev/null 2>&1 || stop "this server has no curl."
  case "$(uname -m)" in
    x86_64 | amd64) ARCHITECTURE="x86_64" ;;
    aarch64 | arm64) ARCHITECTURE="aarch64" ;;
    *) stop "$(uname -m) processors are not supported." ;;
  esac
  memory_kb=$(awk '/^MemTotal:/ { print $2 }' /proc/meminfo)
  # A machine sold as 2 GB reports a little less.
  [ "$memory_kb" -ge $((MINIMUM_MEMORY_GB * 900000)) ] ||
    stop "it needs at least ${MINIMUM_MEMORY_GB} GB of memory."
  disk_parent="$DATA_DIR"
  [ -d "$disk_parent" ] || disk_parent=/var/lib
  disk_kb=$(df -Pk "$disk_parent" | awk 'NR == 2 { print $4 }')
  [ "$disk_kb" -ge $((MINIMUM_DISK_GB * 1000000)) ] ||
    stop "it needs at least ${MINIMUM_DISK_GB} GB of free disk."
}

check_docker() {
  if ! command -v docker >/dev/null 2>&1; then
    ask "Docker is not installed. Install it with Docker's own script?" ||
      stop "Docker is required. Install it, then run this again."
    curl -fsSL https://get.docker.com | sh
  fi
  systemctl enable --now docker >/dev/null 2>&1 || true
  docker info >/dev/null 2>&1 || stop "Docker is installed but not running."
  docker compose version >/dev/null 2>&1 || stop "Docker's Compose plugin is missing."
}

create_user() {
  if ! id "$SERVICE_USER" >/dev/null 2>&1; then
    getent passwd "$SERVICE_ID" >/dev/null && stop "user id $SERVICE_ID is already in use."
    getent group "$SERVICE_ID" >/dev/null && stop "group id $SERVICE_ID is already in use."
    groupadd --gid "$SERVICE_ID" "$SERVICE_USER"
    # The containers' id is above the range useradd expects, which it warns about.
    useradd -K UID_MAX="$SERVICE_ID" --uid "$SERVICE_ID" --gid "$SERVICE_ID" --home-dir "$DATA_DIR" \
      --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER"
  fi
  [ "$(id -u "$SERVICE_USER")" -eq "$SERVICE_ID" ] ||
    stop "the user $SERVICE_USER exists with another id."
  usermod -aG docker "$SERVICE_USER"
}

download_host() {
  base="https://github.com/${REPOSITORY}/releases/latest/download"
  file="commitarium-host-linux-${ARCHITECTURE}"
  folder=$(mktemp -d)
  trap 'rm -rf "$folder"' EXIT
  say "Downloading Commitarium…"
  curl -fsSL -o "$folder/$file" "$base/$file"
  curl -fsSL -o "$folder/$file.sha256" "$base/$file.sha256"
  (cd "$folder" && sha256sum -c "$file.sha256" >/dev/null) ||
    stop "the download did not match its checksum."
  install -d -m 0700 -o "$SERVICE_USER" -g "$SERVICE_USER" "$DATA_DIR" "$DATA_DIR/bin"
  install -m 0755 -o "$SERVICE_USER" -g "$SERVICE_USER" "$folder/$file" "$BINARY.new"
  mv -f "$BINARY.new" "$BINARY"
  ln -sf "$BINARY" "$COMMAND"
}

install_service() {
  cat >"$UNIT" <<UNIT_FILE
[Unit]
Description=Commitarium host
After=docker.service network-online.target
Wants=docker.service network-online.target

[Service]
User=$SERVICE_USER
Group=$SERVICE_USER
Environment=HOME=$DATA_DIR
ExecStart=$BINARY serve --data-dir $DATA_DIR
StateDirectory=commitarium
StateDirectoryMode=0700
Restart=always
RestartSec=5
# Each device request holds connections open while it waits.
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
UNIT_FILE
  systemctl daemon-reload
  systemctl enable "$SERVICE" >/dev/null 2>&1
  systemctl restart "$SERVICE"
}

stack_is_ready() {
  curl -fs -o /dev/null http://127.0.0.1:8080/health || return 1
  running=$("$BINARY" status --data-dir "$DATA_DIR" 2>/dev/null | grep -c '"state": "running"') || return 1
  [ "$running" -ge 3 ]
}

wait_until_ready() {
  say "Starting Commitarium. The first start downloads several GB and can take a while…"
  waited=0
  until stack_is_ready; do
    waited=$((waited + 5))
    [ "$waited" -lt "$READY_SECONDS" ] ||
      stop "it did not start in time. See: journalctl -u $SERVICE"
    sleep 5
  done
}

uninstall() {
  [ "$(id -u)" -eq 0 ] || stop "run it with sudo."
  systemctl disable --now "$SERVICE" >/dev/null 2>&1 || true
  rm -f "$UNIT" "$COMMAND"
  systemctl daemon-reload
  say "The service is removed. Your stack and data are still in $DATA_DIR."
  say "To stop the stack: sudo docker compose -p commitarium down"
}

if [ "${1:-}" = "--uninstall" ]; then
  uninstall
  exit 0
fi

check_machine
check_docker
create_user
download_host
install_service
wait_until_ready
say "Commitarium is running. Check it any time with: sudo commitarium-host status"
say
say "If this server has a firewall, open TCP port 7443, or use Tailscale."
say
"$BINARY" pair --data-dir "$DATA_DIR"
say "To pair another device later: sudo commitarium-host pair"
