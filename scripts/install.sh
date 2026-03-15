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
  echo "Usage: install.sh [--dir /path] [--port 1450] [--user admin] [--pass claw520] [--profile dev] [--install-openclaw] [--install-openclaw-cn]"
  exit 0
fi

OPENCLAW_CN=0

while [ $# -gt 0 ]; do
  case "$1" in
    --dir) INSTALL_DIR="$2"; shift 2;;
    --port) PORT="$2"; shift 2;;
    --user) USER="$2"; shift 2;;
    --pass) PASS="$2"; shift 2;;
    --profile) PROFILE="$2"; shift 2;;
    --install-openclaw) OPENCLAW_INSTALL=1; shift 1;;
    --install-openclaw-cn) OPENCLAW_INSTALL=1; OPENCLAW_CN=1; shift 1;;
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


ensure_go() {
  if command -v go >/dev/null 2>&1; then
    GOV=$(go env GOVERSION | sed 's/^go//')
    MAJOR=$(echo "$GOV" | cut -d. -f1)
    MINOR=$(echo "$GOV" | cut -d. -f2)
    if [[ "$MAJOR" =~ ^[0-9]+$ ]] && [[ "$MINOR" =~ ^[0-9]+$ ]]; then
      if [ "$MAJOR" -gt 1 ] || { [ "$MAJOR" -eq 1 ] && [ "$MINOR" -ge 19 ]; }; then
        return 0
      fi
    fi
  fi
  echo "Installing Go 1.20..."
  GO_TAR=go1.20.14.linux-amd64.tar.gz
  curl -fsSL https://go.dev/dl/$GO_TAR -o /tmp/$GO_TAR
  rm -rf /usr/local/go
  tar -C /usr/local -xzf /tmp/$GO_TAR
  export PATH=/usr/local/go/bin:$PATH
}

ensure_go

if [ "$OPENCLAW_INSTALL" = "1" ] && ! command -v openclaw >/dev/null 2>&1; then
  if [ "$OPENCLAW_CN" = "1" ]; then
    curl -fsSL https://openclaw.ai/install-cn.sh | bash -s -- --no-onboard
  else
    curl -fsSL https://openclaw.ai/install.sh | bash -s -- --no-onboard
  fi
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
