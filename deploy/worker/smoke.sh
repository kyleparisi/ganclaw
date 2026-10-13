#!/bin/bash
# Isolated TLS + real process/artifact smoke check. No production files touched.
set -euo pipefail
binary="${1:?usage: smoke.sh /absolute/path/ganclaw-worker}"
smoke_dir=$(mktemp -d)
broker_pid=''
worker_pid=''
cleanup() {
  [ -z "$worker_pid" ] || kill "$worker_pid" 2>/dev/null || true
  [ -z "$broker_pid" ] || kill "$broker_pid" 2>/dev/null || true
  wait 2>/dev/null || true
  rm -rf "$smoke_dir"
}
trap cleanup EXIT
umask 077
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj '/CN=localhost' \
  -addext 'subjectAltName=DNS:localhost' -keyout "$smoke_dir/key.pem" \
  -out "$smoke_dir/cert.pem" >/dev/null 2>&1
openssl rand -hex 32 >"$smoke_dir/controller.token"
openssl rand -hex 32 >"$smoke_dir/worker.token"
cat >"$smoke_dir/broker.json" <<JSON
{"listen":"127.0.0.1:19443","certificate":"$smoke_dir/cert.pem","key":"$smoke_dir/key.pem","state_dir":"$smoke_dir/broker","controller_token_file":"$smoke_dir/controller.token","workers":{"mac":{"token_file":"$smoke_dir/worker.token","profiles":["smoke","failure"]}}}
JSON
cat >"$smoke_dir/job.sh" <<'SCRIPT'
#!/bin/sh
set -eu
printf 'real job on %s\n' "$(uname -s)"
printf 'round-trip evidence\n' >"$GANCLAW_JOB_DIR/output/evidence.txt"
exit "${1:-0}"
SCRIPT
chmod 700 "$smoke_dir/job.sh"
cat >"$smoke_dir/worker.json" <<JSON
{"url":"https://localhost:19443","name":"mac","token_file":"$smoke_dir/worker.token","state_dir":"$smoke_dir/worker","profiles":{"smoke":{"command":"$smoke_dir/job.sh","cwd":"$smoke_dir"},"failure":{"command":"$smoke_dir/job.sh","args":["7"],"cwd":"$smoke_dir"}}}
JSON
export SSL_CERT_FILE="$smoke_dir/cert.pem"
"$binary" broker --config "$smoke_dir/broker.json" >"$smoke_dir/broker.log" 2>&1 &
broker_pid=$!
"$binary" worker --config "$smoke_dir/worker.json" >"$smoke_dir/worker.log" 2>&1 &
worker_pid=$!
ctl() { "$binary" "$1" --url https://localhost:19443 --token-file "$smoke_dir/controller.token" "${@:2}"; }
# Wait for readiness, with a bounded deadline.
for attempt in $(seq 1 20); do
  if ctl submit --id 11111111111111111111111111111111 --worker mac --profile smoke --timeout 60 >"$smoke_dir/submitted.json" 2>/dev/null; then break; fi
  sleep 1
done
for attempt in $(seq 1 30); do
  ctl status --id 11111111111111111111111111111111 >"$smoke_dir/status.json"
  if python3 -c 'import json,sys;sys.exit(json.load(open(sys.argv[1]))["state"]!="succeeded")' "$smoke_dir/status.json"; then break; fi
  sleep 1
done
python3 -c 'import json,sys;assert json.load(open(sys.argv[1]))["state"]=="succeeded"' "$smoke_dir/status.json"
ctl artifact --id 11111111111111111111111111111111 --output "$smoke_dir/bundle.tar.gz"
python3 - "$smoke_dir/bundle.tar.gz" <<'PY'
import sys,tarfile
with tarfile.open(sys.argv[1]) as bundle:
    assert bundle.extractfile('output/evidence.txt').read()==b'round-trip evidence\n'
    print(bundle.extractfile('job.log').read().decode().strip())
PY
ctl submit --id 22222222222222222222222222222222 --worker mac --profile failure --timeout 60 >/dev/null
for attempt in $(seq 1 30); do
  ctl status --id 22222222222222222222222222222222 >"$smoke_dir/failure.json"
  if python3 -c 'import json,sys;sys.exit(json.load(open(sys.argv[1]))["state"]!="failed")' "$smoke_dir/failure.json"; then break; fi
  sleep 1
done
python3 - "$smoke_dir/failure.json" <<'PY'
import json,sys
job=json.load(open(sys.argv[1]))
assert job['state']=='failed' and job['result']['exit_code']==7,job
print('TLS job/artifact round-trip and exit-7 failure propagation passed')
PY
