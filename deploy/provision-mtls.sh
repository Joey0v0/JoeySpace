#!/bin/sh
# One-time private mTLS provisioning for a new, single-host JoeySpace install.
# Run as the same administrator who owns deploy/.env. Never commit the output.
set -eu
umask 077

deploy_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(dirname -- "$deploy_dir")
env_file="$deploy_dir/.env"
secret_dir=${1:-/opt/joeyspace-secrets}

case "$secret_dir" in
  /*) ;;
  *) echo 'ERROR: certificate directory must be an absolute path' >&2; exit 1 ;;
esac
case "$secret_dir" in
  *[!a-zA-Z0-9/_-]*|'/'|'')
    echo 'ERROR: use a private absolute path containing only letters, digits, /, _ or -' >&2
    exit 1 ;;
esac
case "$secret_dir" in
  "$repo_dir"|"$repo_dir"/*)
    echo 'ERROR: private certificates must be outside the repository' >&2
    exit 1 ;;
esac
if [ ! -f "$env_file" ]; then
  echo 'ERROR: deploy/.env must exist first' >&2
  exit 1
fi
if [ -L "$env_file" ]; then
  echo 'ERROR: deploy/.env must be a regular file, not a symbolic link' >&2
  exit 1
fi
if [ -e "$secret_dir" ]; then
  echo 'ERROR: target already exists; inspect it, do not overwrite certificates' >&2
  exit 1
fi
if ! command -v openssl >/dev/null 2>&1 || ! command -v python3 >/dev/null 2>&1; then
  echo 'ERROR: openssl and python3 are required' >&2
  exit 1
fi

roles='IM_BOT_CERT_DIR im-bot im.go-im.internal server
AGENT_BOT_CERT_DIR agent-bot agent.go-im.internal client
USER_TRIGGER_CERT_DIR user-trigger user.go-im.internal server
IM_TRIGGER_CERT_DIR im-trigger im.go-im.internal server
IM_TRIGGER_USER_CERT_DIR im-trigger-user im.go-im.internal client
AGENT_TRIGGER_CERT_DIR agent-trigger agent.go-im.internal client
WS_NOTIFICATION_CERT_DIR ws-notification ws.go-im.internal server
PUSH_NOTIFICATION_CERT_DIR push-notification push.go-im.internal client
USER_LEAVE_CERT_DIR user-leave user.go-im.internal client
IM_LEAVE_CERT_DIR im-leave im.go-im.internal server
USER_PUSH_CERT_DIR user-push user.go-im.internal server
PUSH_USER_CERT_DIR push-user push.go-im.internal client'

# Reject a partial/existing deployment before writing even the CA key.
printf '%s\n' "$roles" | while read -r variable role dns purpose; do
  if grep -Eq "^${variable}=.+" "$env_file"; then
    echo "ERROR: $variable already has a value; refusing to replace it" >&2
    exit 1
  fi
done

mkdir -m 700 -- "$secret_dir"
if ! openssl req -x509 -newkey rsa:4096 -nodes -sha256 -days 3650 \
    -subj '/CN=JoeySpace Internal CA' \
    -addext 'basicConstraints=critical,CA:TRUE' \
    -addext 'keyUsage=critical,keyCertSign,cRLSign' \
    -keyout "$secret_dir/ca-key.pem" -out "$secret_dir/ca.pem" >/dev/null 2>&1; then
  echo 'ERROR: CA creation failed; inspect the private target directory' >&2
  exit 1
fi

printf '%s\n' "$roles" | while read -r variable role dns purpose; do
  directory="$secret_dir/$role"
  mkdir -m 700 -- "$directory"
  case "$purpose" in
    server) usage=serverAuth; verify=sslserver ;;
    client) usage=clientAuth; verify=sslclient ;;
    *) echo 'ERROR: invalid certificate purpose' >&2; exit 1 ;;
  esac
  cat > "$directory/extensions.cnf" <<EOF
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=$usage
subjectAltName=DNS:$dns
EOF
  if ! openssl req -new -newkey rsa:3072 -nodes -sha256 \
      -subj "/CN=$dns" -keyout "$directory/key.pem" \
      -out "$directory/request.csr" >/dev/null 2>&1; then
    echo "ERROR: key generation failed for $variable" >&2
    exit 1
  fi
  if ! openssl x509 -req -in "$directory/request.csr" \
      -CA "$secret_dir/ca.pem" -CAkey "$secret_dir/ca-key.pem" \
      -CAcreateserial -days 365 -sha256 -extfile "$directory/extensions.cnf" \
      -out "$directory/cert.pem" >/dev/null 2>&1; then
    echo "ERROR: certificate signing failed for $variable" >&2
    exit 1
  fi
  cp -- "$secret_dir/ca.pem" "$directory/ca.pem"
  if ! openssl verify -CAfile "$directory/ca.pem" -purpose "$verify" \
      -verify_hostname "$dns" "$directory/cert.pem" >/dev/null 2>&1; then
    echo "ERROR: certificate verification failed for $variable" >&2
    exit 1
  fi
  rm -- "$directory/request.csr" "$directory/extensions.cnf"
done

# Update only empty certificate variables, in one atomic replacement. The file
# contains other secrets; never print its content or the CA private key.
python3 - "$env_file" "$secret_dir" <<'PY'
import os
import pathlib
import sys
import tempfile

env_file = pathlib.Path(sys.argv[1])
secret_dir = pathlib.Path(sys.argv[2])
roles = {
    'IM_BOT_CERT_DIR': 'im-bot',
    'AGENT_BOT_CERT_DIR': 'agent-bot',
    'USER_TRIGGER_CERT_DIR': 'user-trigger',
    'IM_TRIGGER_CERT_DIR': 'im-trigger',
    'IM_TRIGGER_USER_CERT_DIR': 'im-trigger-user',
    'AGENT_TRIGGER_CERT_DIR': 'agent-trigger',
    'WS_NOTIFICATION_CERT_DIR': 'ws-notification',
    'PUSH_NOTIFICATION_CERT_DIR': 'push-notification',
    'USER_LEAVE_CERT_DIR': 'user-leave',
    'IM_LEAVE_CERT_DIR': 'im-leave',
    'USER_PUSH_CERT_DIR': 'user-push',
    'PUSH_USER_CERT_DIR': 'push-user',
}
lines = env_file.read_text(encoding='utf-8').splitlines(keepends=True)
seen = set()
result = []
for line in lines:
    name = line.split('=', 1)[0].strip() if '=' in line and not line.lstrip().startswith('#') else ''
    if name in roles:
        if name in seen or line.split('=', 1)[1].strip():
            raise SystemExit('ERROR: duplicate or nonempty ' + name + '; .env was not changed')
        result.append(name + '=' + str(secret_dir / roles[name]) + '\n')
        seen.add(name)
    else:
        result.append(line)
for name, role in roles.items():
    if name not in seen:
        if result and not result[-1].endswith('\n'):
            result.append('\n')
        result.append(name + '=' + str(secret_dir / role) + '\n')
fd, tmp = tempfile.mkstemp(prefix='.env.mtls-', dir=env_file.parent)
try:
    os.fchmod(fd, 0o600)
    with os.fdopen(fd, 'w', encoding='utf-8') as output:
        output.writelines(result)
    os.replace(tmp, env_file)
except BaseException:
    if os.path.exists(tmp):
        os.unlink(tmp)
    raise
PY

echo 'READY: 12 private mTLS certificate directories created and deploy/.env updated.'
echo 'Keep the CA private key outside Git and back it up securely.'
