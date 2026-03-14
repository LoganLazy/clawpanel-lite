#!/usr/bin/env bash
set -eo pipefail

HOME=${HOME:-/root}
export HOME
set -u

REPO="https://github.com/LoganLazy/clawpanel-lite.git"
INSTALL_DIR="/opt/clawpanel-lite"
PORT="1450"
USER="admin"
PASS="claw520"
PROFILE=""
OPENCLAW_INSTALL=0

if [ "${1:-}" = "--help" ]; then
  echo "Usage: install.sh [--dir /path] [--port 1450] [--user admin] [--pass claw520] [--profile dev] [--install-openclaw]"
  exit 0
fi

while [ $# -gt 0 ]; do
  case "$1" in
    --dir) INSTALL_DIR="$2"; shift 2;;
    --port) PORT="$2"; shift 2;;
    --user) USER="$2"; shift 2;;
    --pass) PASS="$2"; shift 2;;
    --profile) PROFILE="$2"; shift 2;;
    --install-openclaw) OPENCLAW_INSTALL=1; shift 1;;
    *) shift;;
  esac
done

if ! command -v git >/dev/null 2>&1; then
  if command -v apt >/dev/null 2>&1; then
    apt update && apt install -y git
  elif command -v yum >/dev/null 2>&1; then
    yum install -y git
  else
    echo "Please install git first."; exit 1
  fi
fi

if ! command -v go >/dev/null 2>&1; then
  if command -v apt >/dev/null 2>&1; then
    apt update && apt install -y golang
  elif command -v yum >/dev/null 2>&1; then
    yum install -y golang
  else
    echo "Please install golang first."; exit 1
  fi
fi

if [ "$OPENCLAW_INSTALL" = "1" ] && ! command -v openclaw >/dev/null 2>&1; then
  curl -fsSL https://openclaw.ai/install.sh | bash
fi

mkdir -p "$INSTALL_DIR"
if [ ! -d "$INSTALL_DIR/.git" ]; then
  git clone "$REPO" "$INSTALL_DIR"
else
  git -C "$INSTALL_DIR" pull
fi

cd "$INSTALL_DIR"
go mod tidy

go build -o clawpanel-lite ./cmd/server

cat > /etc/systemd/system/clawpanel-lite.service <<SERVICE
[Unit]
Description=ClawPanel Lite
After=network.target

[Service]
Type=simple
WorkingDirectory=$INSTALL_DIR
ExecStart=$INSTALL_DIR/clawpanel-lite
Environment=CLAWPANEL_PORT=$PORT
Environment=CLAWPANEL_USER=$USER
Environment=CLAWPANEL_PASS=$PASS
Environment=CLAWPANEL_PROFILE=$PROFILE
Restart=always

[Install]
WantedBy=multi-user.target
SERVICE

systemctl daemon-reload
systemctl enable clawpanel-lite
systemctl restart clawpanel-lite

echo "ClawPanel Lite installed. Visit: http://<server-ip>:$PORT"
