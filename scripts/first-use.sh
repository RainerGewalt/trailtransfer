#!/usr/bin/env bash
# First-use check, level L5 in docs/TESTING.md.
#
# Walks through README.md and docs/QUICKSTART.md the way a new user would and
# stops at the first step that does not behave as documented.
#
#   make first-use                  the committed HEAD, from a fresh clone
#   make first-use RELEASE=0.1.1    the published release: tag v0.1.1 from
#                                   GitHub, go install …@v0.1.1, GHCR image 0.1.1
#
# It needs what the README asks of a user: git, Go 1.26 or newer, Docker with
# Compose v2, mosquitto_pub and mosquitto_sub, and free ports 1883 and 9000.
# If the independent verifier trail-evidence is on PATH, the journals are also
# checked with it (L3); otherwise that step says it was skipped.
#
# FIRST_USE_KEEP=1 keeps the working directory for inspection.
set -euo pipefail

RELEASE="${RELEASE:-}"
REPO_URL="https://github.com/RainerGewalt/trailtransfer.git"
IMAGE="ghcr.io/rainergewalt/trailtransfer"
MODULE="github.com/RainerGewalt/trailtransfer/cmd/trailtransfer"
# Safety net for each wait, in seconds. A wait ends on the message it waits
# for; the bound only stops a hang and is never the assertion.
WAIT="${FIRST_USE_WAIT:-120}"
TOPIC="trailtransfer/worker-01"

here="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/trailtransfer-first-use.XXXXXX")"
clone="$work/trailtransfer"
mqtt_log="$work/mqtt.log"
worker_log="$work/worker.log"
sub_pid=""
worker_pid=""
steps=0

# A separate Compose project, so a demo the user already runs is not touched.
export COMPOSE_PROJECT_NAME="trailtransfer-first-use"

step() { steps=$((steps + 1)); printf '\n==> %s\n' "$*"; }
ok() { printf '    ok: %s\n' "$*"; }

diagnostics() {
	if [[ -s $mqtt_log ]]; then
		echo "--- last MQTT messages:" >&2
		tail -n 15 "$mqtt_log" >&2
	fi
	if [[ -s $worker_log ]]; then
		echo "--- locally started worker:" >&2
		tail -n 15 "$worker_log" >&2
	fi
	if [[ -d $clone ]]; then
		echo "--- docker compose logs:" >&2
		(cd "$clone" && docker compose logs --tail 20 2>/dev/null) >&2 || true
	fi
}

fail() {
	printf '\nFIRST-USE FAILED at step %d: %s\n' "$steps" "$*" >&2
	diagnostics
	exit 1
}

cleanup() {
	[[ -n $sub_pid ]] && kill "$sub_pid" 2>/dev/null || true
	[[ -n $worker_pid ]] && kill "$worker_pid" 2>/dev/null || true
	if [[ -d $clone ]]; then
		(cd "$clone" && docker compose down -v --remove-orphans >/dev/null 2>&1) || true
	fi
	if [[ ${FIRST_USE_KEEP:-0} == 1 ]]; then
		echo "kept: $work"
	else
		rm -rf "$work"
	fi
}
trap cleanup EXIT

need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is not installed: $2"; }
port_in_use() { (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; }

# wait_for FILE PATTERN WHAT: wait until FILE has a line matching PATTERN.
wait_for() {
	local file=$1 pattern=$2 what=$3 i
	for ((i = 0; i < WAIT * 5; i++)); do
		grep -Eq -- "$pattern" "$file" 2>/dev/null && return 0
		sleep 0.2
	done
	fail "no $what within ${WAIT}s"
}

# run_cli EXPECTED_EXIT GREP_PATTERN ARGS...: run a command, check its exit
# code and that its output contains the pattern.
run_cli() {
	local want=$1 pattern=$2 out code
	shift 2
	if out="$("$@" 2>&1)"; then code=0; else code=$?; fi
	[[ $code == "$want" ]] || fail "$* exited $code, want $want; output: $out"
	grep -Eq -- "$pattern" <<<"$out" || fail "$* did not print /$pattern/; output: $out"
	ok "$(head -n 1 <<<"$out")  [exit $code: $*]"
}

send() { mosquitto_pub -h localhost -p 1883 -t "$TOPIC/commands" "$@" || fail "mosquitto_pub $*"; }

# result JOB: wait for the job's result event and print it.
result() {
	wait_for "$mqtt_log" "^$TOPIC/jobs/$1/result " "result event for $1"
	grep -E "^$TOPIC/jobs/$1/result " "$mqtt_log" | tail -n 1
}

expect_result() {
	local job=$1 r
	shift
	r="$(result "$job")"
	for pattern in "$@"; do
		grep -q -- "$pattern" <<<"$r" || fail "result of $job lacks $pattern: $r"
	done
	ok "$job: $*"
}

# ---------------------------------------------------------------------------
step "Prerequisites from the README"
need git "it is used to get the code"
need docker "the Quickstart runs on Docker Compose"
docker compose version >/dev/null 2>&1 || fail "Docker Compose v2 (docker compose) is missing"
need go "README, Install: Go 1.26 or newer"
need mosquitto_pub "the Quickstart sends commands with it (package mosquitto-clients)"
need mosquitto_sub "the Quickstart watches the worker with it (package mosquitto-clients)"
gov="$(go env GOVERSION)"
gomin="${gov#go1.}"
gomin="${gomin%%.*}"
[[ $gomin =~ ^[0-9]+$ && $gomin -ge 26 ]] || fail "Go $gov is older than the documented 1.26"
for port in 1883 9000; do
	port_in_use "$port" && fail "port $port is in use; the demo stack publishes MQTT on 1883 and S3 on 9000"
done
ok "git, docker compose, $gov, mosquitto clients; ports 1883 and 9000 free"

# ---------------------------------------------------------------------------
step "Get the code"
if [[ -z $RELEASE ]]; then
	rev="$(git -C "$here" rev-parse HEAD)"
	if [[ -n "$(git -C "$here" status --porcelain)" ]]; then
		echo "    note: uncommitted changes are not part of this check; it runs the committed HEAD ${rev:0:12}"
	fi
	git clone --quiet "$here" "$clone"
	git -C "$clone" checkout --quiet "$rev"
	ok "fresh clone of $(git -C "$clone" describe --tags --always)"
else
	git clone --quiet --depth 1 --branch "v$RELEASE" "$REPO_URL" "$clone" 2>"$work/clone.err" ||
		fail "tag v$RELEASE not found on $REPO_URL: $(cat "$work/clone.err")"
	ok "tag v$RELEASE from GitHub"
fi
cd "$clone"

# ---------------------------------------------------------------------------
step "Install the CLI (README, Install)"
if [[ -z $RELEASE ]]; then
	make build >"$work/build.log" 2>&1 || fail "make build: $(tail -n 5 "$work/build.log")"
	tt="$clone/trailtransfer"
	ok "make build: $("$tt" version)"
else
	GOBIN="$work/bin" go install "$MODULE@v$RELEASE" 2>"$work/install.log" ||
		fail "go install $MODULE@v$RELEASE: $(tail -n 5 "$work/install.log")"
	tt="$work/bin/trailtransfer"
	[[ "$("$tt" version)" == "$RELEASE" ]] || fail "go install binary reports '$("$tt" version)', want $RELEASE"
	ok "go install …@v$RELEASE reports $RELEASE"
	docker pull --quiet "$IMAGE:$RELEASE" >/dev/null || fail "docker pull $IMAGE:$RELEASE"
	iv="$(docker run --rm "$IMAGE:$RELEASE" version)"
	[[ $iv == "$RELEASE" ]] || fail "image $IMAGE:$RELEASE reports '$iv', want $RELEASE"
	ok "image $IMAGE:$RELEASE reports $RELEASE"
	cat >"$work/release.yaml" <<EOF
services:
  trailtransfer:
    image: $IMAGE:$RELEASE
EOF
	export COMPOSE_FILE="docker-compose.yaml:$work/release.yaml"
fi

# ---------------------------------------------------------------------------
step "Start the demo stack (Quickstart 1)"
if [[ -z $RELEASE ]]; then
	docker compose up --build -d >"$work/compose-up.log" 2>&1 ||
		fail "docker compose up --build: $(tail -n 10 "$work/compose-up.log")"
else
	docker compose up --no-build -d >"$work/compose-up.log" 2>&1 ||
		fail "docker compose up with $IMAGE:$RELEASE: $(tail -n 10 "$work/compose-up.log")"
fi
for ((i = 0; i < WAIT * 5; i++)); do
	port_in_use 1883 && break
	sleep 0.2
done
port_in_use 1883 || fail "the broker did not accept connections on 1883 within ${WAIT}s"
ok "stack started"

step "Watch what the worker publishes (Quickstart 2)"
mosquitto_sub -h localhost -p 1883 -v -t "$TOPIC/#" >"$mqtt_log" 2>&1 &
sub_pid=$!
# The retained health message shows both that the subscription is active and
# that the worker is online.
wait_for "$mqtt_log" "^$TOPIC/health .*\"status\":\"healthy\"" "retained health message"
wait_for "$mqtt_log" "^$TOPIC/capabilities " "retained capabilities message"
ok "retained health and capabilities"

# ---------------------------------------------------------------------------
step "Send the documented check command (Quickstart 3)"
send -f examples/commands/check.json
expect_result job-020 '"status":"completed"'

step "Own use case: copy two own files, a third one stays out by filter (README, copy.json)"
head -c 2048 /dev/urandom >upload_folder/first-use-1.png
head -c 3072 /dev/urandom >upload_folder/first-use-2.jpg
printf 'not an image\n' >upload_folder/first-use-notes.txt
send -f examples/commands/copy.json
expect_result job-001 '"status":"completed"' '"files_transferred":2'
listing="$(docker compose exec -T s3 ls /data/trailtransfer)" || fail "docker compose exec s3 ls /data/trailtransfer"
grep -qx first-use-1.png <<<"$listing" && grep -qx first-use-2.jpg <<<"$listing" ||
	fail "the copied files are not in the demo bucket: $listing"
grep -q first-use-notes.txt <<<"$listing" && fail "the filter let first-use-notes.txt through: $listing"
ok "bucket holds first-use-1.png and first-use-2.jpg, not first-use-notes.txt"

step "Refused by policy (README: sync-dry-run.json; Quickstart 3: source /etc)"
send -f examples/commands/sync-dry-run.json
expect_result job-014 '"status":"rejected"' 'allow_sync_delete'
send -m '{"job_id":"demo-2","action":"copy","source":"/etc","target":"s3-demo:trailtransfer","operator":"you"}'
expect_result demo-2 '"status":"rejected"'

# ---------------------------------------------------------------------------
step "Verify the journal (Quickstart 4 and README)"
run_cli 0 '^OK: 4 record\(s\)' docker compose exec -T trailtransfer trailtransfer verify /data/evidence/journal.jsonl
run_cli 0 '^OK: 4 record\(s\)' "$tt" verify evidence/journal.jsonl
awk '!done && sub(/"status":"completed"/, "\"status\":\"failed\"") { done = 1 } 1' \
	evidence/journal.jsonl >"$work/edited.jsonl"
cmp -s evidence/journal.jsonl "$work/edited.jsonl" && fail "could not edit a copy of the journal"
run_cli 1 'hash_mismatch' "$tt" verify "$work/edited.jsonl"

# ---------------------------------------------------------------------------
step "Signed journal: keygen, a worker with a signing key, verify with the right and a wrong key"
docker compose stop trailtransfer >/dev/null 2>&1 || fail "docker compose stop trailtransfer"
mkdir -p "$work/keys" "$work/signed"
"$tt" keygen --out "$work/keys/worker-01" >/dev/null || fail "trailtransfer keygen"
"$tt" keygen --out "$work/keys/other" >/dev/null || fail "trailtransfer keygen"
signed="$work/signed/journal.jsonl"
TRAILTRANSFER_WORKER_ID=worker-01 \
	TRAILTRANSFER_MQTT_HOST=localhost \
	TRAILTRANSFER_MQTT_PORT=1883 \
	TRAILTRANSFER_POLICY_FILE=config/worker-policy.example.yaml \
	TRAILTRANSFER_RCLONE_CONFIG=config/rclone.conf.example \
	TRAILTRANSFER_EVIDENCE_JOURNAL="$signed" \
	TRAILTRANSFER_SIGNING_KEY="$work/keys/worker-01.key" \
	"$tt" run >"$worker_log" 2>&1 &
worker_pid=$!
wait_for "$worker_log" 'subscribed to command topic' "subscription of the locally started worker"
send -m '{"job_id":"job-015","action":"sync","source":"/data/reports","target":"s3-demo:trailtransfer/reports","dry_run":true}'
expect_result job-015 '"status":"rejected"' '"signature"'
kill "$worker_pid"
wait "$worker_pid" 2>/dev/null || true
worker_pid=""
run_cli 0 '1 signature\(s\) verified' "$tt" verify "$signed" --pubkey "$work/keys/worker-01.pub"
run_cli 1 'signature_invalid' "$tt" verify "$signed" --pubkey "$work/keys/other.pub"
run_cli 1 'signature_invalid' "$tt" verify --pubkey "$work/keys/other.pub" "$signed"
run_cli 0 'pass --pubkey to verify signatures' "$tt" verify "$signed"

# ---------------------------------------------------------------------------
step "Independent verifier trail-evidence (L3)"
if command -v trail-evidence >/dev/null 2>&1; then
	run_cli 0 '^VALID' trail-evidence verify evidence/journal.jsonl
	run_cli 0 '^VALID' trail-evidence verify "$signed" --pubkey "$work/keys/worker-01.pub" --require-signatures
	run_cli 1 '^INVALID' trail-evidence verify "$work/edited.jsonl"
else
	echo "    skipped: trail-evidence is not on PATH (go install github.com/RainerGewalt/trail-evidence/go/cmd/trail-evidence@latest)"
fi

if [[ -n $RELEASE ]]; then
	printf '\nFIRST-USE PASSED: %d steps, published release %s\n' "$steps" "$RELEASE"
else
	printf '\nFIRST-USE PASSED: %d steps, committed HEAD from a fresh clone\n' "$steps"
fi
