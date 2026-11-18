#!/usr/bin/env bash
# Negative-path scenarios, level L4 in docs/TESTING.md, without Docker.
#
# Runs the worker natively against a local Mosquitto and a local rclone, with
# an rclone alias remote on a temporary folder as the target, and checks what
# the worker does at the edges of what its documentation promises: a target
# outside the allowed path, a source behind a symbolic link, a job_id that is
# not a valid MQTT topic level, a dry run, a file over the size limit, move
# without allow_delete, a status query after a failed job, a journal that
# cannot be written, a policy with a typo, and the restart cases of issue #1:
# a worker killed during a transfer, a worker stopped during a transfer, and
# a result sealed while the broker is away and the worker restarts, and a
# second worker on the same journal.
#
#   make negative-paths                         builds the working tree
#   TT=/path/to/trailtransfer make negative-paths   checks another binary
#
# Every check runs its own worker with its own worker_id and journal. Each
# check prints PASS or FAIL with what it saw, and the script continues after
# a FAIL so that one run shows the state of every check; it exits 1 if any
# check failed.
#
# Needs: mosquitto, mosquitto_pub, mosquitto_sub and rclone on PATH (or
# MOSQUITTO, RCLONE), go unless TT is given, and a free local port
# (NEG_PORT, default 15931). Ports below 15000 are never used.
# NEG_KEEP=1 keeps the working directory.
set -u

PORT="${NEG_PORT:-15931}"
# Safety net for each wait, in seconds. A wait ends on the message it waits
# for; the bound only stops a hang and is never the assertion.
WAIT="${NEG_WAIT:-20}"
MOSQUITTO="${MOSQUITTO:-$(command -v mosquitto || echo /usr/sbin/mosquitto)}"
RCLONE="${RCLONE:-$(command -v rclone || true)}"

here="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/trailtransfer-negative.XXXXXX")"
mqtt_log="$work/mqtt.log"
broker_log="$work/broker.log"
broker_pid=""
sub_pid=""
worker_pid=""
passed=0
failed=0
current=""

die() {
	printf 'negative-paths: %s\n' "$*" >&2
	exit 2
}

cleanup() {
	[[ -n $worker_pid ]] && kill -9 "$worker_pid" 2>/dev/null
	[[ -n $sub_pid ]] && kill "$sub_pid" 2>/dev/null
	[[ -n $broker_pid ]] && kill "$broker_pid" 2>/dev/null
	wait 2>/dev/null
	if [[ ${NEG_KEEP:-0} == 1 ]]; then
		echo "kept: $work"
	else
		rm -rf "$work"
	fi
}
trap cleanup EXIT

check() {
	current="$1"
	printf '\n==> %s\n' "$current"
}
pass() {
	passed=$((passed + 1))
	printf '    PASS: %s\n' "$*"
}
fail() {
	failed=$((failed + 1))
	printf '    FAIL: %s\n' "$*"
}
note() { printf '    note: %s\n' "$*"; }

# wait_for FILE PATTERN: wait until FILE has a line matching PATTERN; 1 on timeout.
wait_for() {
	local file=$1 pattern=$2 i
	for ((i = 0; i < WAIT * 5; i++)); do
		grep -Eq -- "$pattern" "$file" 2>/dev/null && return 0
		sleep 0.2
	done
	return 1
}

port_in_use() { (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; }

send() { mosquitto_pub -h 127.0.0.1 -p "$PORT" -q 1 -t "trailtransfer/$1/commands" -m "$2"; }

# topic_lines WORKER JOB SUFFIX: the MQTT lines of one job topic.
topic_lines() { grep -E "^trailtransfer/$1/jobs/$2/$3 " "$mqtt_log" 2>/dev/null; }

# wait_topic WORKER JOB SUFFIX: wait for the first message on a job topic.
wait_topic() { wait_for "$mqtt_log" "^trailtransfer/$1/jobs/$2/$3 "; }

# write_policy FILE WORKER: the policy every check starts from.
write_policy() {
	cat >"$1" <<EOF
worker_id: $2
policy_version: "negative-1"
allowed_actions: [copy, move, check]
allowed_sources: [$work/src]
allowed_targets: [archive:incoming]
max_file_size_mb: 1
max_parallel_jobs: 2
allow_delete: false
allow_sync_delete: false
allow_absolute_targets: false
allow_broadcast_commands: false
dry_run_default: true
require_job_id: true
EOF
}

# start_worker WORKER [FSIZE_KB]: start a worker with its own policy and
# journal; with FSIZE_KB the worker runs under ulimit -f.
start_worker() {
	local id=$1 limit=${2:-}
	mkdir -p "$work/$id"
	[[ -f $work/$id/policy.yaml ]] || write_policy "$work/$id/policy.yaml" "$id"
	: >"$work/$id/worker.log"
	(
		export TRAILTRANSFER_WORKER_ID="$id" \
			TRAILTRANSFER_MQTT_HOST=127.0.0.1 \
			TRAILTRANSFER_MQTT_PORT="$PORT" \
			TRAILTRANSFER_POLICY_FILE="$work/$id/policy.yaml" \
			TRAILTRANSFER_RCLONE_CONFIG="$work/rclone.conf" \
			TRAILTRANSFER_RCLONE_BINARY="${WORKER_RCLONE:-$RCLONE}" \
			TRAILTRANSFER_EVIDENCE_JOURNAL="$work/$id/journal.jsonl" \
			TRAILTRANSFER_HEALTH_INTERVAL_SECONDS=1
		[[ -n $limit ]] && ulimit -f "$limit"
		exec "$TT" run
	) >>"$work/$id/worker.log" 2>&1 &
	worker_pid=$!
	wait_for "$work/$id/worker.log" 'subscribed to command topic' ||
		die "worker $id did not subscribe within ${WAIT}s: $(tail -n 3 "$work/$id/worker.log")"
}

# stop_worker: SIGTERM, then SIGKILL after 5 s. Prints how it ended.
stop_worker() {
	local i
	[[ -z $worker_pid ]] && return 0
	kill -TERM "$worker_pid" 2>/dev/null
	for ((i = 0; i < 25; i++)); do
		kill -0 "$worker_pid" 2>/dev/null || break
		sleep 0.2
	done
	if kill -0 "$worker_pid" 2>/dev/null; then
		kill -9 "$worker_pid" 2>/dev/null
		wait "$worker_pid" 2>/dev/null
		worker_pid=""
		return 1
	fi
	wait "$worker_pid" 2>/dev/null
	worker_pid=""
	return 0
}

# ---------------------------------------------------------------------------
for tool in mosquitto_pub mosquitto_sub; do
	command -v "$tool" >/dev/null 2>&1 || die "$tool is not installed (package mosquitto-clients)"
done
[[ -x $MOSQUITTO ]] || die "mosquitto is not installed (set MOSQUITTO)"
[[ -n $RCLONE && -x $RCLONE ]] || die "rclone is not installed (set RCLONE)"
[[ $PORT -ge 15000 ]] || die "NEG_PORT must be 15000 or higher"
port_in_use "$PORT" && die "port $PORT is in use (set NEG_PORT)"

if [[ -z ${TT:-} ]]; then
	command -v go >/dev/null 2>&1 || die "go is not installed and TT is not set"
	TT="$work/trailtransfer"
	(cd "$here" && go build -o "$TT" ./cmd/trailtransfer) || die "go build failed"
fi
echo "binary: $TT ($("$TT" version))"
echo "rclone: $RCLONE ($("$RCLONE" version 2>/dev/null | head -n 1))"

mkdir -p "$work/src" "$work/target/incoming" "$work/outside"
head -c 2048 /dev/urandom >"$work/src/a.png"
printf 'notes\n' >"$work/src/notes.txt"
head -c 2097152 /dev/urandom >"$work/src/big.bin"
printf 'outside the allowed source\n' >"$work/outside/secret.txt"
ln -s "$work/outside" "$work/src/link"
cat >"$work/rclone.conf" <<EOF
[archive]
type = alias
remote = $work/target
EOF
cat >"$work/mosquitto.conf" <<EOF
listener $PORT 127.0.0.1
allow_anonymous true
EOF
"$MOSQUITTO" -c "$work/mosquitto.conf" >"$broker_log" 2>&1 &
broker_pid=$!
for ((i = 0; i < WAIT * 5; i++)); do
	port_in_use "$PORT" && break
	sleep 0.2
done
port_in_use "$PORT" || die "mosquitto did not listen on $PORT: $(tail -n 3 "$broker_log")"
mosquitto_sub -h 127.0.0.1 -p "$PORT" -v -t 'trailtransfer/#' >"$mqtt_log" 2>&1 &
sub_pid=$!
for ((i = 0; i < WAIT * 5; i++)); do
	mosquitto_pub -h 127.0.0.1 -p "$PORT" -t trailtransfer/probe -m ready
	grep -q '^trailtransfer/probe ready' "$mqtt_log" && break
	sleep 0.2
done
grep -q '^trailtransfer/probe ready' "$mqtt_log" || die "the subscriber did not receive its probe message"

# ---------------------------------------------------------------------------
check "S1: a target that leaves the allowed path is refused"
start_worker neg-s1
send neg-s1 '{"job_id":"s1-dots","action":"copy","source":"'"$work"'/src","target":"archive:incoming/../../escaped","filters":["a.png"],"dry_run":false}'
send neg-s1 '{"job_id":"s1-ok","action":"copy","source":"'"$work"'/src","target":"archive:incoming//./ok/","filters":["a.png"],"dry_run":false}'
if wait_topic neg-s1 s1-dots result; then
	r="$(topic_lines neg-s1 s1-dots result)"
	# rclone resolves the dots inside the alias root (target/escaped) or, for
	# a plain local path, above it (escaped); both are outside the allowed path.
	escaped="$(ls "$work/target/escaped/a.png" "$work/escaped/a.png" 2>/dev/null)"
	if grep -q '"status":"rejected"' <<<"$r" && [[ -z $escaped ]]; then
		pass "archive:incoming/../../escaped rejected, nothing written outside archive:incoming"
	else
		fail "archive:incoming/../../escaped was not refused: $(grep -o '"status":"[a-z]*"' <<<"$r" | head -n 1), written outside archive:incoming: ${escaped#$work/}"
	fi
else
	fail "no result for s1-dots within ${WAIT}s"
fi
if wait_topic neg-s1 s1-ok result && topic_lines neg-s1 s1-ok result | grep -q '"status":"completed"' && [[ -f $work/target/incoming/ok/a.png ]]; then
	pass "archive:incoming//./ok/ is the same place as archive:incoming/ok and is allowed"
else
	fail "archive:incoming//./ok/ did not complete into target/incoming/ok: $(topic_lines neg-s1 s1-ok result | grep -o '"status":"[a-z]*"' | head -n 1)"
fi
stop_worker || note "worker did not stop within 5 s after SIGTERM"

# ---------------------------------------------------------------------------
check "S1: a source behind a symbolic link that leaves the allowed root is refused"
start_worker neg-link
send neg-link '{"job_id":"link-1","action":"copy","source":"'"$work"'/src/link","target":"archive:incoming/link","dry_run":false}'
if wait_topic neg-link link-1 result; then
	r="$(topic_lines neg-link link-1 result)"
	if grep -q '"status":"rejected"' <<<"$r" && [[ ! -e $work/target/incoming/link/secret.txt ]]; then
		pass "source src/link -> outside rejected, secret.txt not copied"
	else
		fail "source src/link -> outside was not refused: $(grep -o '"status":"[a-z]*"' <<<"$r" | head -n 1), copied: $([[ -e $work/target/incoming/link/secret.txt ]] && echo yes || echo no)"
	fi
else
	fail "no result for link-1 within ${WAIT}s"
fi
stop_worker || note "worker did not stop within 5 s after SIGTERM"

# ---------------------------------------------------------------------------
check "E2: a dry run is recorded as a dry run, not as a transfer"
start_worker neg-e2
send neg-e2 '{"job_id":"e2-dry","action":"copy","source":"'"$work"'/src","target":"archive:incoming/dry","filters":["notes.txt"]}'
if wait_topic neg-e2 e2-dry result; then
	r="$(topic_lines neg-e2 e2-dry result)"
	if grep -q '"dry_run":true' <<<"$r" && grep -q '"files_transferred":0' <<<"$r" &&
		grep -q '"bytes_transferred":0' <<<"$r" && [[ ! -e $work/target/incoming/dry/notes.txt ]]; then
		pass "record has dry_run true, files_transferred 0, bytes_transferred 0; nothing at the target"
	else
		fail "dry run recorded as: $(grep -oE '"(dry_run|files_transferred|bytes_transferred)":[a-z0-9]*' <<<"$r" | tr '\n' ' ')"
	fi
else
	fail "no result for e2-dry within ${WAIT}s"
fi
stop_worker || note "worker did not stop within 5 s after SIGTERM"

# ---------------------------------------------------------------------------
check "E7: a file over max_file_size_mb does not end in a completed record without a note"
start_worker neg-e7
send neg-e7 '{"job_id":"e7-big","action":"copy","source":"'"$work"'/src","target":"archive:incoming/big","filters":["big.bin"],"dry_run":false}'
if wait_topic neg-e7 e7-big result; then
	r="$(topic_lines neg-e7 e7-big result)"
	if grep -q '"max_file_size_mb":1' <<<"$r"; then
		pass "record names the size limit that excluded big.bin ($(grep -oE '"(status|files_transferred|max_file_size_mb)":"?[a-z0-9]*' <<<"$r" | tr '\n' ' '))"
	else
		fail "record has no note of the size limit: $(grep -oE '"(status|files_transferred|bytes_transferred)":"?[a-z0-9]*' <<<"$r" | tr '\n' ' ')"
	fi
else
	fail "no result for e7-big within ${WAIT}s"
fi
stop_worker || note "worker did not stop within 5 s after SIGTERM"

# ---------------------------------------------------------------------------
check "S2: move is refused while allow_delete is false"
start_worker neg-s2
send neg-s2 '{"job_id":"s2-move","action":"move","source":"'"$work"'/src","target":"archive:incoming/moved","filters":["notes.txt"],"dry_run":false}'
if wait_topic neg-s2 s2-move result; then
	r="$(topic_lines neg-s2 s2-move result)"
	if grep -q '"status":"rejected"' <<<"$r" && grep -q 'allow_delete' <<<"$r" && [[ -f $work/src/notes.txt ]]; then
		pass "move rejected with allow_delete in the reason; src/notes.txt still there"
	else
		fail "move was not refused: $(grep -o '"status":"[a-z]*"' <<<"$r" | head -n 1), src/notes.txt present: $([[ -f $work/src/notes.txt ]] && echo yes || echo no)"
	fi
else
	fail "no result for s2-move within ${WAIT}s"
fi
stop_worker || note "worker did not stop within 5 s after SIGTERM"
[[ -f $work/src/notes.txt ]] || printf 'notes\n' >"$work/src/notes.txt"

# ---------------------------------------------------------------------------
check "E1: a job_id that is not a valid topic level neither disconnects nor blocks the worker"
start_worker neg-e1
before="$(grep -c 'trailtransfer-neg-e1' "$broker_log")"
send neg-e1 '{"job_id":"bad#id","action":"copy","source":"/etc","target":"archive:incoming"}'
send neg-e1 '{"job_id":"run#id","action":"check","source":"'"$work"'/src","target":"archive:incoming","filters":["a.png"]}'
send neg-e1 '{"job_id":"bad+id","action":"copy","source":"/etc","target":"archive:incoming"}'
send neg-e1 '{"job_id":"a/b","action":"copy","source":"/etc","target":"archive:incoming"}'
send neg-e1 '{"job_id":"e1-after","action":"copy","source":"/etc","target":"archive:incoming"}'
if wait_topic neg-e1 e1-after result; then
	pass "a valid command after the invalid ones is answered"
else
	fail "no answer to e1-after within ${WAIT}s after the invalid job_ids"
fi
if grep -E 'trailtransfer-neg-e1' "$broker_log" | tail -n +"$((before + 1))" | grep -qiE 'socket error|disconnect'; then
	fail "the broker disconnected the worker: $(grep -E 'trailtransfer-neg-e1' "$broker_log" | grep -iE 'socket error|disconnect' | head -n 1)"
else
	pass "the broker did not disconnect the worker"
fi
if grep -q 'run#id' "$work/neg-e1/journal.jsonl" 2>/dev/null; then
	fail "the job with job_id run#id ran and was sealed"
else
	pass "no job ran for an invalid job_id"
fi
if stop_worker; then
	pass "worker stopped within 5 s after SIGTERM"
else
	fail "worker did not stop within 5 s after SIGTERM (killed)"
fi

# ---------------------------------------------------------------------------
check "E8: a status query reports the stored outcome on its own topic only"
start_worker neg-e8
send neg-e8 '{"job_id":"e8-fail","action":"check","source":"'"$work"'/src","target":"archive:incoming/nothing-here","filters":["a.png"]}'
if wait_topic neg-e8 e8-fail result && topic_lines neg-e8 e8-fail result | grep -q '"status":"failed"'; then
	wait_for "$mqtt_log" '^trailtransfer/neg-e8/jobs/e8-fail/status .*"status":"failed"'
	n_before="$(topic_lines neg-e8 e8-fail status | wc -l)"
	send neg-e8 '{"job_id":"e8-query","action":"status","target_job_id":"e8-fail"}'
	if wait_topic neg-e8 e8-query status; then
		r="$(topic_lines neg-e8 e8-query status)"
		n_after="$(topic_lines neg-e8 e8-fail status | wc -l)"
		if grep -q '"target_status":"failed"' <<<"$r"; then
			pass "the answer on e8-query/status says failed"
		else
			fail "the answer does not report failed: $r"
		fi
		if [[ $n_after == "$n_before" ]]; then
			pass "nothing was published on e8-fail/status by the query"
		else
			fail "the query published on e8-fail/status: $(topic_lines neg-e8 e8-fail status | tail -n 1)"
		fi
	else
		fail "no answer on e8-query/status within ${WAIT}s"
	fi
	send neg-e8 '{"job_id":"e8-query-2","action":"status","target_job_id":"never-seen"}'
	if wait_topic neg-e8 e8-query-2 status && ! grep -q '^trailtransfer/neg-e8/jobs/never-seen/' "$mqtt_log"; then
		pass "a query for an unknown job_id writes nothing to that job's topics"
	else
		fail "a query for an unknown job_id wrote to its topics or got no answer"
	fi
else
	fail "setup: e8-fail did not end as failed: $(topic_lines neg-e8 e8-fail result | grep -o '"status":"[a-z]*"' | head -n 1)"
fi
stop_worker || note "worker did not stop within 5 s after SIGTERM"

# ---------------------------------------------------------------------------
check "E6: a journal write failure stops sealing, is reported and refuses new jobs"
start_worker neg-e6
send neg-e6 '{"job_id":"e6-pre-1","action":"copy","source":"/etc","target":"archive:incoming"}'
send neg-e6 '{"job_id":"e6-pre-2","action":"copy","source":"/etc","target":"archive:incoming"}'
wait_topic neg-e6 e6-pre-2 result || die "setup: no result for e6-pre-2"
stop_worker || note "worker did not stop within 5 s after SIGTERM"
size="$(stat -c %s "$work/neg-e6/journal.jsonl")"
limit=$((size / 1024))
note "journal has $size bytes; the worker now runs under ulimit -f $limit (KiB), so the next append fails"
start_worker neg-e6 "$limit"
send neg-e6 '{"job_id":"e6-1","action":"copy","source":"/etc","target":"archive:incoming"}'
if wait_for "$mqtt_log" '^trailtransfer/neg-e6/jobs/e6-1/status .*"status":"rejected"'; then
	if topic_lines neg-e6 e6-1 result | grep -vq '"content_hash"'; then
		fail "an unsealed result was published for e6-1"
	else
		pass "no unsealed result was published for e6-1"
	fi
else
	fail "no status for e6-1 within ${WAIT}s"
fi
if grep -q '"level":"ERROR"' "$work/neg-e6/worker.log"; then
	pass "the worker logged an ERROR: $(grep '"level":"ERROR"' "$work/neg-e6/worker.log" | head -n 1 | cut -c1-160)"
else
	fail "the worker logged no ERROR"
fi
if wait_for "$mqtt_log" '^trailtransfer/neg-e6/health .*"status":"unhealthy"'; then
	pass "health reports unhealthy"
else
	fail "health did not leave healthy: $(grep '^trailtransfer/neg-e6/health ' "$mqtt_log" | tail -n 1 | cut -c1-120)"
fi
send neg-e6 '{"job_id":"e6-2","action":"check","source":"'"$work"'/src","target":"archive:incoming","filters":["a.png"]}'
if wait_for "$mqtt_log" '^trailtransfer/neg-e6/jobs/e6-2/status .*"status":"(rejected|completed|failed)"'; then
	if topic_lines neg-e6 e6-2 status | grep -q '"status":"rejected"' && ! topic_lines neg-e6 e6-2 status | grep -q '"status":"started"'; then
		pass "the next job is refused: $(topic_lines neg-e6 e6-2 status | head -n 1 | grep -o '"detail":"[^"]*"')"
	else
		fail "the next job was accepted: $(topic_lines neg-e6 e6-2 status | grep -o '"status":"[a-z]*"' | tr '\n' ' ')"
	fi
else
	fail "no status for e6-2 within ${WAIT}s"
fi
stop_worker || note "worker did not stop within 5 s after SIGTERM"
if "$TT" verify "$work/neg-e6/journal.jsonl" >"$work/neg-e6/verify.txt" 2>&1; then
	pass "the journal is still intact: $(head -n 1 "$work/neg-e6/verify.txt")"
else
	fail "the journal no longer verifies: $(head -n 3 "$work/neg-e6/verify.txt" | tr '\n' ' ')"
fi

# ---------------------------------------------------------------------------
check "S4: validate-config fails on a policy with a typo or an unknown action"
mkdir -p "$work/s4"
write_policy "$work/s4/typo.yaml" neg-s4
sed -i 's/^dry_run_default:/dry_run_defualt:/' "$work/s4/typo.yaml"
printf 'policy_version: "1"\nallowed_actions: [copy, delete, rm]\n' >"$work/s4/actions.yaml"
for f in typo actions; do
	out="$(TRAILTRANSFER_WORKER_ID=neg-s4 TRAILTRANSFER_MQTT_HOST=127.0.0.1 TRAILTRANSFER_MQTT_PORT="$PORT" \
		TRAILTRANSFER_POLICY_FILE="$work/s4/$f.yaml" TRAILTRANSFER_RCLONE_CONFIG="$work/rclone.conf" \
		"$TT" validate-config 2>&1)"
	code=$?
	if [[ $code == 1 ]]; then
		pass "$f.yaml: exit 1, $out"
	else
		fail "$f.yaml: exit $code, $out"
	fi
done

# ---------------------------------------------------------------------------
# Restart cases (issue #1). A transfer that is still running when the worker
# stops is played by a stand-in for rclone that records its PID and waits.
printf '#!/bin/sh\necho $$ >"%s/slow-rclone.pid"\nexec sleep 60\n' "$work" >"$work/slow-rclone"
printf '#!/bin/sh\nsleep 3\nexit 0\n' >"$work/brief-rclone"
chmod +x "$work/slow-rclone" "$work/brief-rclone"
copy_job() { printf '{"job_id":"%s","action":"copy","source":"%s/src","target":"archive:incoming","filters":["a.png"],"dry_run":false}' "$1" "$work"; }
journal_count() { grep -c "\"job_id\":\"$2\"" "$work/$1/journal.jsonl" 2>/dev/null || true; }

check "R1: a worker killed during a transfer leaves exactly one interrupted record and does not run the job again"
rm -f "$work/slow-rclone.pid"
WORKER_RCLONE="$work/slow-rclone" start_worker neg-r1
send neg-r1 "$(copy_job r1-1)"
wait_for "$mqtt_log" '^trailtransfer/neg-r1/jobs/r1-1/status .*"status":"started"' || die "setup: r1-1 did not start"
wait_for "$work/slow-rclone.pid" '^[0-9]+$' || die "setup: the rclone stand-in did not start"
child="$(cat "$work/slow-rclone.pid")"
kill -9 "$worker_pid" 2>/dev/null
wait "$worker_pid" 2>/dev/null
worker_pid=""
if [[ $(journal_count neg-r1 r1-1) == 0 ]]; then
	pass "the killed worker left no record for r1-1"
else
	fail "the killed worker left $(journal_count neg-r1 r1-1) record(s) for r1-1"
fi
gone=1
for ((i = 0; i < 25; i++)); do
	kill -0 "$child" 2>/dev/null || {
		gone=0
		break
	}
	sleep 0.2
done
if [[ $gone == 0 ]]; then
	pass "the transfer process (pid $child) ended with the worker"
elif [[ $(uname -s) == Linux ]]; then
	fail "the transfer process (pid $child) outlived the worker"
	kill -9 "$child" 2>/dev/null
else
	note "the transfer process outlived the worker; it is bound to the worker on Linux only"
	kill -9 "$child" 2>/dev/null
fi
start_worker neg-r1
if wait_for "$mqtt_log" '^trailtransfer/neg-r1/jobs/r1-1/result .*"event_type":"transfer.job.interrupted"'; then
	line="$(topic_lines neg-r1 r1-1 result | grep 'transfer.job.interrupted' | head -n 1)"
	if grep -q '"status":"interrupted"' <<<"$line" && grep -q '"reason":"worker_restart"' <<<"$line"; then
		pass "the next start sealed and published transfer.job.interrupted with reason worker_restart"
	else
		fail "unexpected interrupted record: $(cut -c1-200 <<<"$line")"
	fi
else
	fail "no transfer.job.interrupted for r1-1 within ${WAIT}s after the restart"
fi
send neg-r1 "$(copy_job r1-1)"
if wait_for "$mqtt_log" '^trailtransfer/neg-r1/jobs/r1-1/result .*"reason":"duplicate_job_id"'; then
	started="$(topic_lines neg-r1 r1-1 status | grep -c '"status":"started"')"
	detail="$(topic_lines neg-r1 r1-1 result | grep duplicate_job_id | head -n 1 | grep -o '"detail":"[^"]*"')"
	if [[ $started == 1 ]] && grep -q 'last status: interrupted' <<<"$detail"; then
		pass "the redelivered command was refused and not started again: $detail"
	else
		fail "redelivered command: started $started time(s), $detail"
	fi
else
	fail "the redelivered command was not answered as a duplicate within ${WAIT}s"
fi
stop_worker || note "worker did not stop within 5 s after SIGTERM"
start_worker neg-r1
stop_worker || note "worker did not stop within 5 s after SIGTERM"
n="$(grep -c '"event_type":"transfer.job.interrupted"' "$work/neg-r1/journal.jsonl")"
if [[ $n == 1 ]]; then
	pass "after two more starts the journal still holds exactly one interrupted record"
else
	fail "the journal holds $n interrupted records"
fi
if "$TT" verify "$work/neg-r1/journal.jsonl" >"$work/neg-r1/verify.txt" 2>&1; then
	pass "the journal verifies: $(head -n 1 "$work/neg-r1/verify.txt")"
else
	fail "the journal does not verify: $(head -n 3 "$work/neg-r1/verify.txt" | tr '\n' ' ')"
fi

# ---------------------------------------------------------------------------
check "R2: SIGTERM during a transfer ends it and seals the job as interrupted before the worker exits"
rm -f "$work/slow-rclone.pid"
WORKER_RCLONE="$work/slow-rclone" start_worker neg-r2
send neg-r2 "$(copy_job r2-1)"
wait_for "$mqtt_log" '^trailtransfer/neg-r2/jobs/r2-1/status .*"status":"started"' || die "setup: r2-1 did not start"
wait_for "$work/slow-rclone.pid" '^[0-9]+$' || die "setup: the rclone stand-in did not start"
child="$(cat "$work/slow-rclone.pid")"
if stop_worker; then
	pass "the worker stopped within 5 s after SIGTERM"
else
	fail "the worker did not stop within 5 s after SIGTERM"
fi
if kill -0 "$child" 2>/dev/null; then
	fail "the transfer process (pid $child) is still running"
	kill -9 "$child" 2>/dev/null
else
	pass "the transfer process was ended"
fi
line="$(grep '"job_id":"r2-1"' "$work/neg-r2/journal.jsonl" | head -n 1)"
if [[ $(journal_count neg-r2 r2-1) == 1 ]] && grep -q '"event_type":"transfer.job.interrupted"' <<<"$line" && grep -q '"reason":"worker_shutdown"' <<<"$line"; then
	pass "the journal holds one transfer.job.interrupted with reason worker_shutdown"
else
	fail "journal for r2-1: $(journal_count neg-r2 r2-1) record(s), $(cut -c1-200 <<<"$line")"
fi
if wait_for "$mqtt_log" '^trailtransfer/neg-r2/jobs/r2-1/result .*"reason":"worker_shutdown"'; then
	pass "the record was published before the worker exited"
else
	fail "the record was not published"
fi
start_worker neg-r2
stop_worker || note "worker did not stop within 5 s after SIGTERM"
if [[ $(journal_count neg-r2 r2-1) == 1 ]] && "$TT" verify "$work/neg-r2/journal.jsonl" >"$work/neg-r2/verify.txt" 2>&1; then
	pass "the next start adds nothing for r2-1 and the journal verifies: $(head -n 1 "$work/neg-r2/verify.txt")"
else
	fail "after the next start: $(journal_count neg-r2 r2-1) record(s) for r2-1, $(head -n 2 "$work/neg-r2/verify.txt" 2>/dev/null | tr '\n' ' ')"
fi

# ---------------------------------------------------------------------------
check "R3: a result sealed while the broker is away is published after a restart of the worker"
WORKER_RCLONE="$work/brief-rclone" start_worker neg-r3
send neg-r3 "$(copy_job r3-1)"
wait_for "$mqtt_log" '^trailtransfer/neg-r3/jobs/r3-1/status .*"status":"started"' || die "setup: r3-1 did not start"
kill "$sub_pid" 2>/dev/null
kill "$broker_pid" 2>/dev/null
wait "$broker_pid" 2>/dev/null
broker_pid=""
if wait_for "$work/neg-r3/journal.jsonl" '"job_id":"r3-1"'; then
	pass "the outcome was sealed while the broker was away: $(grep -o '"status":"[a-z]*"' "$work/neg-r3/journal.jsonl" | tail -n 1)"
else
	fail "no record for r3-1 in the journal within ${WAIT}s"
fi
if topic_lines neg-r3 r3-1 result | grep -q .; then
	fail "a result for r3-1 was published before the broker went away; the check proves nothing"
fi
if stop_worker; then
	pass "the worker stopped within 5 s after SIGTERM although the broker was away"
else
	fail "the worker did not stop within 5 s after SIGTERM while the broker was away"
fi
"$MOSQUITTO" -c "$work/mosquitto.conf" >>"$broker_log" 2>&1 &
broker_pid=$!
for ((i = 0; i < WAIT * 5; i++)); do
	port_in_use "$PORT" && break
	sleep 0.2
done
port_in_use "$PORT" || die "mosquitto did not come back on $PORT"
mosquitto_sub -h 127.0.0.1 -p "$PORT" -v -t 'trailtransfer/#' >>"$mqtt_log" 2>&1 &
sub_pid=$!
for ((i = 0; i < WAIT * 5; i++)); do
	mosquitto_pub -h 127.0.0.1 -p "$PORT" -t trailtransfer/probe -m back
	grep -q '^trailtransfer/probe back' "$mqtt_log" && break
	sleep 0.2
done
start_worker neg-r3
if wait_topic neg-r3 r3-1 result; then
	n="$(topic_lines neg-r3 r3-1 result | grep -c '"event_type":"transfer.job.result"')"
	pass "the new worker published the sealed result of r3-1 ($n message(s))"
else
	fail "the result of r3-1 was not published within ${WAIT}s after broker and worker were back"
fi
stop_worker || note "worker did not stop within 5 s after SIGTERM"
if [[ $(journal_count neg-r3 r3-1) == 1 ]] && "$TT" verify "$work/neg-r3/journal.jsonl" >"$work/neg-r3/verify.txt" 2>&1; then
	pass "the journal holds one record for r3-1 and verifies: $(head -n 1 "$work/neg-r3/verify.txt")"
else
	fail "journal for r3-1: $(journal_count neg-r3 r3-1) record(s), $(head -n 2 "$work/neg-r3/verify.txt" 2>/dev/null | tr '\n' ' ')"
fi

# ---------------------------------------------------------------------------
check "R4: a second worker on the same journal does not start"
start_worker neg-r4
out="$(TRAILTRANSFER_WORKER_ID=neg-r4 TRAILTRANSFER_MQTT_HOST=127.0.0.1 TRAILTRANSFER_MQTT_PORT="$PORT" \
	TRAILTRANSFER_POLICY_FILE="$work/neg-r4/policy.yaml" TRAILTRANSFER_RCLONE_CONFIG="$work/rclone.conf" \
	TRAILTRANSFER_RCLONE_BINARY="$RCLONE" TRAILTRANSFER_EVIDENCE_JOURNAL="$work/neg-r4/journal.jsonl" \
	timeout 10 "$TT" run 2>&1)"
code=$?
if [[ $code == 1 ]] && grep -q 'in use by another worker' <<<"$out"; then
	pass "exit 1: $(tail -n 1 <<<"$out" | cut -c1-160)"
elif [[ $(uname -s) != Linux && $(uname -s) != Darwin ]]; then
	note "not enforced on this system (exit $code)"
else
	fail "the second worker ran (exit $code): $(tail -n 1 <<<"$out" | cut -c1-160)"
fi
send neg-r4 "$(copy_job r4-1)"
if wait_topic neg-r4 r4-1 result; then
	pass "the first worker keeps working"
else
	fail "the first worker no longer answers"
fi
stop_worker || note "worker did not stop within 5 s after SIGTERM"

# ---------------------------------------------------------------------------
printf '\nNEGATIVE PATHS: %d passed, %d failed (binary %s)\n' "$passed" "$failed" "$("$TT" version)"
[[ $failed == 0 ]]
