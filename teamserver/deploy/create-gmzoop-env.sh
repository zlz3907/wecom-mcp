#!/bin/sh
# Creates the protected Connector API Key environment file on the target host.
# GNAS credentials stay as placeholders for the server Secret manager to inject.
set -eu

usage() { echo "usage: $0 /etc/wecom-mcp/gmzoop.env" >&2; exit 2; }
[ "$#" -eq 1 ] || usage
target=$1
case "$target" in /etc/wecom-mcp/gmzoop.env) ;; *) usage ;; esac
[ ! -e "$target" ] || { echo "error: target already exists" >&2; exit 1; }
command -v openssl >/dev/null 2>&1 || { echo "error: openssl is required" >&2; exit 1; }

install -d -o root -g root -m 0750 /etc/wecom-mcp
temporary=$(mktemp /etc/wecom-mcp/.gmzoop.env.XXXXXX)
trap 'rm -f "$temporary"' EXIT HUP INT TERM
connector_key=$(openssl rand -hex 32)
audit_key=$(openssl rand -hex 32)
umask 077
{
  printf '%s\n' 'TEAM_MCP_PUBLIC_URL=https://mcp.jyiai.com/gmzoop'
  printf '%s\n' 'TEAM_MCP_LISTEN_ADDR=127.0.0.1:7702'
  printf '%s\n' 'TEAM_MCP_AUTH_MODE=connector_api_key'
  printf 'TEAM_MCP_CONNECTOR_API_KEY=%s\n' "$connector_key"
  printf '%s\n' 'TEAM_MCP_CONNECTOR_ROLE=admin'
  printf '%s\n' 'TEAM_MCP_USER_AUTHZ_ENABLED=false'
  printf 'TEAM_MCP_AUDIT_HMAC_KEY=%s\n' "$audit_key"
  printf '%s\n' 'TEAM_MCP_MAX_CONCURRENT_TOOLS=2'
  printf '%s\n' 'GNAS_BASE_URL=https://jyiai.com'
  printf '%s\n' 'GNAS_APP_ID=<INJECT_FROM_SERVER_SECRET_MANAGER>'
  printf '%s\n' 'GNAS_APP_SECRET=<INJECT_FROM_SERVER_SECRET_MANAGER>'
} > "$temporary"
chown root:root "$temporary"
chmod 0600 "$temporary"
mv "$temporary" "$target"
trap - EXIT HUP INT TERM
printf '%s\n' 'env_created=yes connector_key=not_printed next=inject_gnas_credentials'
