#!/usr/bin/env bash
#
# Install or upgrade phish_wu on an Ubuntu server.
#
# Topology this sets up:
#
#   nginx  :443 ──► 127.0.0.1:3333   admin interface (TLS, self-signed, loopback only)
#          :443 ──► 127.0.0.1:8080   phishing server (plain HTTP, loopback only)
#
# gophish binds only unprivileged loopback ports, so it never needs root and
# never needs setcap - nginx owns 80/443. Mail goes out through an external
# SMTP provider configured in the web UI, so no local MTA is installed.
#
# Re-running this script upgrades in place: gophish.db, config.json and the
# generated admin certificate are all left alone.
#
set -euo pipefail

REPO="tonylin2026-debug/phish_wu"
INSTALL_DIR="/opt/gophish"
SERVICE_USER="gophish"
SERVICE_NAME="gophish"
ADMIN_LISTEN="127.0.0.1:3333"
PHISH_LISTEN="127.0.0.1:8080"
ADMIN_DOMAIN=""
CONTACT_ADDRESS=""
PACKAGE=""
RELEASE=""
START_SERVICE=1

say()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[!]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[x]\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
    cat <<'USAGE'
Usage: install.sh [options]

Source of the build (pick one):
  --package <path|url>     A gophish-*-linux-64bit.zip to install
  --release <tag|latest>   Download that release from GitHub instead

Configuration (only used when config.json does not already exist):
  --admin-domain <host>    Public hostname of the admin interface. Required
                           behind a reverse proxy or CSRF will reject logins.
  --contact-address <mail> Shown to recipients who query the transparency
                           endpoint, and sent as X-Gophish-Contact.

Placement:
  --install-dir <path>     Default: /opt/gophish
  --user <name>            Default: gophish
  --admin-listen <ip:port> Default: 127.0.0.1:3333
  --phish-listen <ip:port> Default: 127.0.0.1:8080

Other:
  --no-start               Install but do not start the service
  -h, --help               This message

Example:
  sudo ./install.sh --release latest \
       --admin-domain admin.example.com \
       --contact-address security@example.com
USAGE
}

while [ $# -gt 0 ]; do
    case "$1" in
        --package)         PACKAGE="$2"; shift 2 ;;
        --release)         RELEASE="$2"; shift 2 ;;
        --admin-domain)    ADMIN_DOMAIN="$2"; shift 2 ;;
        --contact-address) CONTACT_ADDRESS="$2"; shift 2 ;;
        --install-dir)     INSTALL_DIR="$2"; shift 2 ;;
        --user)            SERVICE_USER="$2"; shift 2 ;;
        --admin-listen)    ADMIN_LISTEN="$2"; shift 2 ;;
        --phish-listen)    PHISH_LISTEN="$2"; shift 2 ;;
        --no-start)        START_SERVICE=0; shift ;;
        -h|--help)         usage; exit 0 ;;
        *)                 usage >&2; die "unknown option: $1" ;;
    esac
done

# ---------------------------------------------------------------- checks ----

[ "$(id -u)" -eq 0 ] || die "run this with sudo"

if [ -r /etc/os-release ]; then
    # shellcheck source=/dev/null
    . /etc/os-release
    [ "${ID:-}" = "ubuntu" ] || warn "this script targets Ubuntu; found ${PRETTY_NAME:-unknown}"
else
    warn "cannot identify the distribution, continuing anyway"
fi

[ -n "$PACKAGE" ] || [ -n "$RELEASE" ] || { usage >&2; die "need --package or --release"; }
[ -z "$PACKAGE" ] || [ -z "$RELEASE" ] || die "--package and --release are mutually exclusive"

# ---------------------------------------------------------- dependencies ----

say "Installing dependencies"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq unzip ca-certificates curl >/dev/null

# ------------------------------------------------------------- the build ----

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

if [ -n "$RELEASE" ]; then
    if [ "$RELEASE" = "latest" ]; then
        say "Looking up the latest release of $REPO"
        RELEASE="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
                   | grep -m1 '"tag_name"' | cut -d'"' -f4)"
        [ -n "$RELEASE" ] || die "could not determine the latest release tag"
    fi
    PACKAGE="https://github.com/$REPO/releases/download/$RELEASE/gophish-$RELEASE-linux-64bit.zip"
    say "Using release $RELEASE"
fi

case "$PACKAGE" in
    http://*|https://*)
        say "Downloading $PACKAGE"
        curl -fsSL -o "$WORK/gophish.zip" "$PACKAGE" \
            || die "download failed - check the release tag and that the asset exists"
        ;;
    *)
        [ -f "$PACKAGE" ] || die "no such file: $PACKAGE"
        cp "$PACKAGE" "$WORK/gophish.zip"
        ;;
esac

say "Unpacking"
mkdir -p "$WORK/unpacked"
unzip -q "$WORK/gophish.zip" -d "$WORK/unpacked"

# gophish resolves VERSION, templates/, static/ and db/ relative to its working
# directory, so a package missing any of them produces a confusing runtime
# failure rather than an obvious one. Check here instead.
for required in gophish VERSION config.json db templates static; do
    [ -e "$WORK/unpacked/$required" ] \
        || die "the package is missing '$required' - is this a gophish release zip?"
done
[ -f "$WORK/unpacked/static/db/geolite2-city.mmdb" ] \
    || warn "the GeoIP database is missing; results will not be geolocated"

VERSION_STR="$(tr -d '[:space:]' < "$WORK/unpacked/VERSION")"
say "Package looks good (version $VERSION_STR)"

# ----------------------------------------------------------------- user -----

if id "$SERVICE_USER" >/dev/null 2>&1; then
    say "User $SERVICE_USER already exists"
else
    say "Creating system user $SERVICE_USER"
    useradd --system --home-dir "$INSTALL_DIR" --shell /usr/sbin/nologin "$SERVICE_USER"
fi

# -------------------------------------------------------------- install -----

UPGRADE=0
[ -f "$INSTALL_DIR/gophish" ] && UPGRADE=1

if [ "$UPGRADE" -eq 1 ] && systemctl is-active --quiet "$SERVICE_NAME"; then
    say "Stopping $SERVICE_NAME for the upgrade"
    systemctl stop "$SERVICE_NAME"
fi

say "Installing into $INSTALL_DIR"
mkdir -p "$INSTALL_DIR"

# A release zip ships its own config.json, with the phishing server on
# 0.0.0.0:80. That is never what we want here: either the operator already has
# a configuration we must not touch, or we are about to write a fresh one. Drop
# it before copying, otherwise it lands in the install directory and the check
# below cannot tell it apart from a real existing config.
rm -f "$WORK/unpacked/config.json"

# Copy over the top rather than replacing the directory: gophish.db and the
# generated admin certificate live here and must survive an upgrade.
cp -a "$WORK/unpacked/." "$INSTALL_DIR/"

# ---------------------------------------------------------------- config ----

if [ -f "$INSTALL_DIR/config.json" ]; then
    say "Keeping the existing config.json"
else
    say "Writing config.json"

    # A fixed CSRF key. Left empty gophish generates a new one on every start,
    # which invalidates every open form and login the moment the service
    # restarts.
    CSRF_KEY="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"

    TRUSTED="[]"
    if [ -n "$ADMIN_DOMAIN" ]; then
        TRUSTED="[\"$ADMIN_DOMAIN\"]"
    else
        warn "no --admin-domain given. Behind a reverse proxy, logins will fail"
        warn "the CSRF origin check until you add it to trusted_origins."
    fi

    cat > "$INSTALL_DIR/config.json" <<EOF
{
    "admin_server": {
        "listen_url": "$ADMIN_LISTEN",
        "use_tls": true,
        "cert_path": "gophish_admin.crt",
        "key_path": "gophish_admin.key",
        "csrf_key": "$CSRF_KEY",
        "trusted_origins": $TRUSTED,
        "allowed_internal_hosts": []
    },
    "phish_server": {
        "listen_url": "$PHISH_LISTEN",
        "use_tls": false,
        "cert_path": "example.crt",
        "key_path": "example.key"
    },
    "db_name": "sqlite3",
    "db_path": "gophish.db",
    "migrations_prefix": "db/db_",
    "contact_address": "$CONTACT_ADDRESS",
    "logging": {
        "filename": "",
        "level": "info"
    }
}
EOF
fi

chown -R "$SERVICE_USER:$SERVICE_USER" "$INSTALL_DIR"
chmod 750 "$INSTALL_DIR"
# The config carries the CSRF key, and on a MySQL setup it would carry database
# credentials too.
chmod 640 "$INSTALL_DIR/config.json"
chmod 750 "$INSTALL_DIR/gophish"

# ---------------------------------------------------------------- systemd ---

say "Installing the systemd unit"
cat > "/etc/systemd/system/$SERVICE_NAME.service" <<EOF
[Unit]
Description=phish_wu phishing simulation server
Documentation=https://github.com/$REPO
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SERVICE_USER
Group=$SERVICE_USER
# gophish reads VERSION, templates/, static/ and db/ by relative path. If this
# is wrong it will not start.
WorkingDirectory=$INSTALL_DIR
ExecStart=$INSTALL_DIR/gophish
Restart=on-failure
RestartSec=5

NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
# AF_UNIX is needed as well: this binary is built with cgo, so Go may use the
# system resolver, which talks to nscd or systemd-resolved over a unix socket.
# Without it, hostname lookups for the SMTP and IMAP servers fail.
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
RestrictNamespaces=true
LockPersonality=true
MemoryDenyWriteExecute=true
ReadWritePaths=$INSTALL_DIR

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable "$SERVICE_NAME" >/dev/null 2>&1

if [ "$START_SERVICE" -eq 0 ]; then
    say "Installed. Not starting, as requested."
    exit 0
fi

say "Starting $SERVICE_NAME"
systemctl restart "$SERVICE_NAME"

ADMIN_PORT="${ADMIN_LISTEN##*:}"
ADMIN_HOST="${ADMIN_LISTEN%:*}"
for _ in $(seq 1 60); do
    if curl -sk --max-time 2 "https://$ADMIN_HOST:$ADMIN_PORT/login" >/dev/null 2>&1; then
        break
    fi
    if ! systemctl is-active --quiet "$SERVICE_NAME"; then
        warn "the service stopped during startup:"
        journalctl -u "$SERVICE_NAME" --no-pager -n 40 >&2
        die "startup failed"
    fi
    sleep 1
done

systemctl is-active --quiet "$SERVICE_NAME" || die "the service is not running"
say "Service is up"

# ---------------------------------------------------------------- summary ---

echo
echo "─────────────────────────────────────────────────────────────"
echo " phish_wu $VERSION_STR installed in $INSTALL_DIR"
echo
echo "   admin interface : https://$ADMIN_LISTEN  (loopback only)"
echo "   phishing server : http://$PHISH_LISTEN   (loopback only)"
echo "   database        : $INSTALL_DIR/gophish.db  (SQLite)"
echo "   service         : systemctl status $SERVICE_NAME"
echo "   logs            : journalctl -u $SERVICE_NAME -f"
echo "─────────────────────────────────────────────────────────────"

if [ "$UPGRADE" -eq 0 ]; then
    PASSWORD_LINE="$(journalctl -u "$SERVICE_NAME" --no-pager 2>/dev/null \
                     | grep -m1 'Please login with the username' || true)"
    echo
    if [ -n "$PASSWORD_LINE" ]; then
        echo " Initial credentials (you will be forced to change the password):"
        echo
        echo "   ${PASSWORD_LINE#*msg=\"}" | sed 's/"$//'
    else
        echo " Find the initial password with:"
        echo "   journalctl -u $SERVICE_NAME | grep 'Please login'"
    fi
    echo
    echo " Next:"
    echo "   1. Put nginx in front - see deploy/nginx.conf.example"
    echo "   2. Open 80/443 only; leave ${ADMIN_PORT} and ${PHISH_LISTEN##*:} closed"
    echo "   3. Add your external SMTP provider under Sending Profiles"
    echo
fi
