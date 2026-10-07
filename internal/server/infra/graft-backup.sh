#!/bin/bash
# Managed by graft - overwritten on every "graft db <name> backup" command.
# Usage: graft-backup.sh <db> <action> [args]
#   run | scheduled | prune | check | info | plain | log
#   presign [file] [seconds] | test-restore [file] [inspect] | restore [file]
set -euo pipefail
umask 077

DB="${1:-}"
ACTION="${2:-}"
shift 2 || true

if ! [[ "$DB" =~ ^[a-zA-Z0-9_]+$ ]]; then
  echo "❌ invalid database name" >&2
  exit 2
fi

BASE=/opt/graft/infra/backup
ENVF="$BASE/$DB.env"
STATUS="$BASE/$DB.status"
VERIFIED="$BASE/$DB.verified"
LOGF="$BASE/$DB.log"
LASTCOUNTS="$BASE/$DB.lastverify.counts"

if [ ! -f "$ENVF" ]; then
  echo "❌ no backup config for '$DB' - run: graft db $DB backup set" >&2
  exit 2
fi

# Read KEY=value without sourcing, so values are never interpreted by the shell.
cfg() { grep -m1 "^$1=" "$ENVF" | cut -d= -f2- || true; }

BUCKET=$(cfg S3_BUCKET)
ENDPOINT=$(cfg S3_ENDPOINT)
PGU=$(cfg PGUSER)
RETENTION=$(cfg RETENTION_DAYS)
SCHED_TZ=$(cfg SCHEDULE_TZ)
SCHED_HOURS=$(cfg SCHEDULE_HOURS)
SCHED_MIN=$(cfg SCHEDULE_MINUTE)
PREFIX="backups/$DB/"
TGF="$BASE/$DB.telegram"
LASTOK="$BASE/$DB.lastok"
ALERTED="$BASE/$DB.alerted"
# Telegram settings live in their own file so they never reach the aws-cli container.
tgcfg() { if [ -f "$TGF" ]; then grep -m1 "^$1=" "$TGF" | cut -d= -f2- || true; fi; }
TG_TOKEN=$(tgcfg TELEGRAM_BOT_TOKEN)
TG_CHAT=$(tgcfg TELEGRAM_CHAT_ID)
ALERT=0

TRACK=0
FAIL_MSG=""
LAST_FILE=""
LAST_SIZE=""
CNAME=""
WORK=""
PASS=0
WARN=0
BAD=0

# Keep the log from growing without bound.
if [ -f "$LOGF" ] && [ "$(stat -c%s "$LOGF")" -gt 1048576 ]; then
  tail -n 2000 "$LOGF" > "$LOGF.tmp" && mv "$LOGF.tmp" "$LOGF"
fi

# Everything the user should be able to audit goes through log(): screen + file.
log() { printf '[%s] %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" | tee -a "$LOGF"; }
ok() { PASS=$((PASS + 1)); log "   ✔ $*"; }
warn() { WARN=$((WARN + 1)); log "   ⚠ $*"; }
bad() { BAD=$((BAD + 1)); log "   ✘ $*"; }
layer() { log "[layer $1/7] $2"; }

drop_testdb() {
  if [ -n "$CNAME" ]; then
    sudo docker rm -f "$CNAME" >/dev/null 2>&1 || true
    CNAME=""
    log "🗑️  Throwaway test database removed."
  fi
}

cleanup() {
  if [ -n "$CNAME" ]; then
    sudo docker rm -f "$CNAME" >/dev/null 2>&1 || true
  fi
  if [ -n "$WORK" ] && [ -d "$WORK" ]; then
    rm -rf "$WORK" 2>/dev/null || sudo rm -rf "$WORK" || true
  fi
}

# Sends a Telegram message. Never fails the caller: an alert problem must not
# turn a good backup into a failed one. The token goes to curl on stdin so it
# does not show up in the process list.
notify() {
  if [ -z "$TG_TOKEN" ] || [ -z "$TG_CHAT" ]; then
    return 0
  fi
  if printf 'url = "https://api.telegram.org/bot%s/sendMessage"\n' "$TG_TOKEN" \
    | curl -fsS -m 20 -K - --data-urlencode "chat_id=$TG_CHAT" --data-urlencode "text=$1" >/dev/null 2>&1; then
    log "   📨 Telegram alert sent"
  else
    log "   ⚠ could not send the Telegram alert (check token, chat id and that curl is installed)"
  fi
  return 0
}

on_exit() {
  local rc=$?
  if [ "$TRACK" = 1 ]; then
    if [ "$rc" -eq 0 ]; then
      echo "$(date -u +%FT%TZ) OK $LAST_FILE $LAST_SIZE" > "$STATUS"
      date +%s > "$LASTOK"
      rm -f "$ALERTED"
    else
      echo "$(date -u +%FT%TZ) FAIL ${FAIL_MSG:-exit $rc}" > "$STATUS"
    fi
  fi
  if [ "$rc" -ne 0 ] && [ "$ALERT" = 1 ]; then
    notify "❌ graft: $ACTION of database '$DB' FAILED on $(hostname)"$'\n'"${FAIL_MSG:-exit code $rc}"$'\n'"Details: graft db $DB backup log"
  fi
  cleanup
}

# Called every hour by cron, including hours with no backup due. Raises an alarm
# when no backup has succeeded for more than two intervals (cron stopped, keys
# expired, disk full...), at most once a day so it never spams.
stale_check() {
  local base now count interval_h limit
  if [ -f "$LASTOK" ]; then
    base=$(cat "$LASTOK")
  else
    base=$(stat -c %Y "$ENVF")
  fi
  now=$(date +%s)
  interval_h=24
  if [ -n "$SCHED_HOURS" ]; then
    count=$(echo "$SCHED_HOURS" | tr ',' '\n' | grep -c . || true)
    if [ "$count" -ge 1 ]; then
      interval_h=$((24 / count))
    fi
  fi
  limit=$(((2 * interval_h + 1) * 3600))
  if [ $((now - base)) -le "$limit" ]; then
    return 0
  fi
  if [ -f "$ALERTED" ] && [ $((now - $(stat -c %Y "$ALERTED"))) -lt 86400 ]; then
    return 0
  fi
  log "🚨 no successful backup of '$DB' for $(((now - base) / 3600))h (expected every ${interval_h}h)"
  notify "🚨 graft: no successful backup of '$DB' on $(hostname) for $(((now - base) / 3600))h (expected every ${interval_h}h)"$'\n'"Check: graft db $DB backup list"
  touch "$ALERTED"
}
trap on_exit EXIT

fail() {
  log "❌ $*"
  FAIL_MSG="$*"
  exit 1
}

mkwork() {
  if [ -n "$WORK" ]; then
    return 0
  fi
  mkdir -p "$BASE/tmp"
  WORK=$(mktemp -d "$BASE/tmp/$DB.XXXXXX")
}

aws() {
  local ep=()
  if [ -n "$ENDPOINT" ]; then
    ep=(--endpoint-url "$ENDPOINT")
  fi
  local mount=()
  if [ -n "$WORK" ]; then
    mount=(-v "$WORK:/work")
  fi
  sudo docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp --env-file "$ENVF" \
    ${mount[@]+"${mount[@]}"} amazon/aws-cli ${ep[@]+"${ep[@]}"} "$@"
}

# Prints "<file>\t<size>\t<iso-time>" per backup of this database, oldest first.
list_backups() {
  local out
  out=$(aws s3api list-objects-v2 --bucket "$BUCKET" --prefix "$PREFIX" \
    --query 'Contents[].[Key,Size,LastModified]' --output text) || fail "could not list s3://$BUCKET/$PREFIX"
  echo "$out" | awk -F'\t' -v p="$PREFIX" -v db="$DB" '
    NF >= 3 {
      f = substr($1, length(p) + 1)
      if (f ~ ("^" db "_[0-9]{8}T[0-9]{6}Z\\.dump$")) print f "\t" $2 "\t" $3
    }' | sort
}

latest_backup() {
  list_backups | tail -n 1 | cut -f1
}

# Short stable id for a backup: first 8 hex chars of the SHA-1 of its file name.
id_of() { printf '%s' "$1" | sha1sum | cut -c1-8; }


# Accepts a file name, "latest", or an 8-character id from "backup list".
pick_backup() {
  local want="${1:-}" f matches
  if [ -z "$want" ] || [ "$want" = "latest" ]; then
    want=$(latest_backup)
    [ -n "$want" ] || fail "no backups found for '$DB'"
  elif [[ "$want" =~ ^[0-9a-f]{8}$ ]]; then
    matches=""
    while IFS=$'\t' read -r f _; do
      if [ -n "$f" ] && [ "$(id_of "$f")" = "$want" ]; then
        matches="$matches $f"
      fi
    done < <(list_backups)
    matches="${matches# }"
    [ -n "$matches" ] || fail "no backup has id '$want' - see: graft db $DB backup list"
    [[ "$matches" != *" "* ]] || fail "id '$want' is ambiguous: $matches"
    want="$matches"
  fi
  if ! [[ "$want" =~ ^${DB}_[0-9]{8}T[0-9]{6}Z\.dump$ ]]; then
    fail "'$want' is not a backup file of '$DB'"
  fi
  echo "$want"
}

# Epoch seconds from a backup file name (always UTC).
file_epoch() {
  local s="${1#"${DB}"_}"
  s="${s%.dump}"
  date -u -d "${s:0:4}-${s:4:2}-${s:6:2} ${s:9:2}:${s:11:2}:${s:13:2}" +%s
}

do_run() {
  TRACK=1
  mkwork
  local ts file size
  ts=$(date -u +%Y%m%dT%H%M%SZ)
  file="${DB}_${ts}.dump"

  log "══ Backup of '$DB' ══"
  log "🐘 Dumping '$DB' (pg_dump custom format)..."
  sudo docker exec graft-postgres pg_dump -U "$PGU" -Fc "$DB" > "$WORK/$file" \
    || fail "pg_dump failed for '$DB'"
  [ -s "$WORK/$file" ] || fail "dump of '$DB' is empty"
  size=$(stat -c%s "$WORK/$file")
  log "   dump written: $size bytes"

  # A readable table of contents proves the archive is structurally intact.
  sudo docker exec -i graft-postgres pg_restore --list < "$WORK/$file" > /dev/null \
    || fail "dump of '$DB' failed the integrity check"
  log "   ✔ archive table of contents is readable"

  log "📤 Uploading $file to s3://$BUCKET/$PREFIX ..."
  aws s3 cp "/work/$file" "s3://$BUCKET/$PREFIX$file" --only-show-errors || fail "upload to s3://$BUCKET failed"

  local remote
  remote=$(aws s3api head-object --bucket "$BUCKET" --key "$PREFIX$file" --query ContentLength --output text) \
    || fail "uploaded file could not be found in the bucket"
  [ "$remote" = "$size" ] || fail "uploaded size ($remote) does not match local size ($size)"
  log "   ✔ bucket confirms $remote bytes"

  LAST_FILE="$file"
  LAST_SIZE="$size"
  log "✅ Backup complete: $file"
}

do_prune() {
  if ! [[ "$RETENTION" =~ ^[0-9]+$ ]] || [ "$RETENTION" -lt 1 ]; then
    fail "invalid retention '$RETENTION'"
  fi
  local cutoff total old kept f ts
  cutoff=$(date -u -d "$RETENTION days ago" +%Y%m%dT%H%M%SZ)
  log "🧹 Pruning backups older than $RETENTION days (before $cutoff)..."
  total=0
  old=()
  while IFS=$'\t' read -r f _; do
    if [ -z "$f" ]; then
      continue
    fi
    total=$((total + 1))
    ts="${f#"${DB}"_}"
    ts="${ts%.dump}"
    if [[ "$ts" < "$cutoff" ]]; then
      old+=("$f")
    fi
  done < <(list_backups)

  kept=$((total - ${#old[@]}))
  if [ "${#old[@]}" -eq 0 ]; then
    log "   nothing to prune ($total backup(s) kept)"
    return 0
  fi
  if [ "$kept" -eq 0 ]; then
    # Never leave the bucket without a backup: spare the newest one.
    log "   ⚠ every backup is older than $RETENTION days - keeping the newest so a copy always exists"
    unset 'old[${#old[@]}-1]'
  fi
  for f in ${old[@]+"${old[@]}"}; do
    log "   deleting $f"
    aws s3 rm "s3://$BUCKET/$PREFIX$f" --only-show-errors || fail "could not delete $f"
  done
  log "✅ Pruned ${#old[@]} backup(s); $((total - ${#old[@]})) remain."
}

do_check() {
  mkwork
  local key="${PREFIX}.graft-write-test"
  aws s3api head-bucket --bucket "$BUCKET" || fail "cannot access bucket '$BUCKET' (check endpoint, region and keys)"
  echo ok > "$WORK/probe"
  aws s3 cp /work/probe "s3://$BUCKET/$key" --only-show-errors || fail "bucket '$BUCKET' is not writable with these keys"
  aws s3 rm "s3://$BUCKET/$key" --only-show-errors || fail "bucket '$BUCKET' allows writes but not deletes - pruning would fail"
  log "✅ Bucket '$BUCKET' is reachable, writable and prunable."
}

# One line per backup for the CLI menu: file, size, ISO time, last verified.
do_plain() {
  local f s t v
  while IFS=$'\t' read -r f s t; do
    if [ -z "$f" ]; then
      continue
    fi
    v="-"
    if [ -f "$VERIFIED" ]; then
      v=$(grep "^$f " "$VERIFIED" | tail -n 1 | cut -d' ' -f2- || true)
      if [ -z "$v" ]; then
        v="-"
      fi
    fi
    printf '%s\t%s\t%s\t%s\t%s\n' "$f" "$s" "$t" "$v" "$(id_of "$f")"
  done < <(list_backups)
}

# Keeps exactly one backup (the stable version) and deletes every other one,
# whatever its age. Normal retention still applies to the kept backup later: once
# it is older than RETENTION_DAYS a regular prune removes it too. It must have
# passed a test restore: deleting every other copy for an untested file would
# trade known-good backups for an unknown one.
do_keep_only() {
  local keep f others=()
  keep=$(pick_backup "${1:-}")
  if ! grep -q "^$keep " "$VERIFIED" 2>/dev/null; then
    fail "$keep has never passed a test restore - run: graft db $DB backup test (nothing was deleted)"
  fi
  while IFS=$'\t' read -r f _; do
    if [ -n "$f" ] && [ "$f" != "$keep" ]; then
      others+=("$f")
    fi
  done < <(list_backups)

  log "🧹 Keeping only $keep (id $(id_of "$keep")); removing ${#others[@]} other backup(s)."
  for f in ${others[@]+"${others[@]}"}; do
    log "   deleting $f"
    aws s3 rm "s3://$BUCKET/$PREFIX$f" --only-show-errors || fail "could not delete $f"
  done
  log "✅ Done. Only $keep remains; new backups continue as scheduled."
  notify "🧹 graft: backups of '$DB' on $(hostname) were pruned to a single backup: $keep ($((${#others[@]})) deleted)."
}

# Header for "backup list"; the table itself is rendered by the CLI from "plain".
do_info() {
  echo "Database:  $DB"
  echo "Bucket:    s3://$BUCKET/$PREFIX"
  echo "Retention: $RETENTION days"
  if [ -n "$SCHED_HOURS" ]; then
    echo "Schedule:  hours $SCHED_HOURS at minute $SCHED_MIN ($SCHED_TZ)"
  fi
  if [ -f "$STATUS" ]; then
    echo "Last run:  $(cat "$STATUS")"
  fi
}

do_presign() {
  local file secs
  file=$(pick_backup "${1:-}")
  secs="${2:-3600}"
  if ! [[ "$secs" =~ ^[0-9]+$ ]] || [ "$secs" -lt 60 ] || [ "$secs" -gt 604800 ]; then
    fail "expiry must be between 60 seconds and 7 days"
  fi
  aws s3api head-object --bucket "$BUCKET" --key "$PREFIX$file" >/dev/null || fail "$file not found in the bucket"
  echo "🔗 Download link for $file (valid for $((secs / 60)) minutes):"
  aws s3 presign "s3://$BUCKET/$PREFIX$file" --expires-in "$secs"
}

# --- verification -----------------------------------------------------------

COUNTS_SQL="SELECT table_schema||'.'||table_name, (xpath('/row/c/text()', query_to_xml(format('SELECT count(*) AS c FROM %I.%I', table_schema, table_name), false, true, '')))[1]::text FROM information_schema.tables WHERE table_type='BASE TABLE' AND table_schema NOT IN ('pg_catalog','information_schema') ORDER BY 1"
STAMPS_SQL="SELECT c.table_schema||'.'||c.table_name||'.'||c.column_name, (xpath('/row/m/text()', query_to_xml(format('SELECT max(%I)::text AS m FROM %I.%I', c.column_name, c.table_schema, c.table_name), false, true, '')))[1]::text FROM information_schema.columns c JOIN information_schema.tables t USING (table_schema, table_name) WHERE t.table_type='BASE TABLE' AND c.column_name IN ('created_at','updated_at') AND c.data_type LIKE 'timestamp%' AND c.table_schema NOT IN ('pg_catalog','information_schema') ORDER BY 1 LIMIT 20"

test_psql() { sudo docker exec "$CNAME" psql -U postgres -d restore_test -tA -F "$(printf '\t')" -c "$1"; }
live_psql() { sudo docker exec graft-postgres psql -U "$PGU" -d "$DB" -tA -F "$(printf '\t')" -c "$1"; }

# Seven layers, each logged. Returns non-zero if any layer found a hard failure,
# in which case nothing is recorded as verified. Leaves the throwaway database
# running (callers remove it with drop_testdb).
verify_backup() {
  local file="$1" remote size i toc entries
  PASS=0
  WARN=0
  BAD=0
  mkwork
  log "══ Verifying $file ══"

  layer 1 "Download and size check"
  if [ ! -f "$WORK/$file" ]; then
    if ! aws s3 cp "s3://$BUCKET/$PREFIX$file" "/work/$file" --only-show-errors; then
      bad "download of $file failed"
      return 1
    fi
  fi
  size=$(stat -c%s "$WORK/$file")
  if ! remote=$(aws s3api head-object --bucket "$BUCKET" --key "$PREFIX$file" --query ContentLength --output text); then
    bad "could not read the object size from the bucket"
    return 1
  fi
  if [ "$remote" != "$size" ]; then
    bad "downloaded $size bytes but the bucket holds $remote - truncated or corrupt transfer"
    return 1
  fi
  ok "downloaded $size bytes, identical to the bucket's size"

  layer 2 "Archive signature and table of contents"
  if [ "$(head -c 5 "$WORK/$file")" != "PGDMP" ]; then
    bad "file does not start with the PostgreSQL archive signature (PGDMP)"
    return 1
  fi
  ok "PostgreSQL custom-format signature present"
  toc="$WORK/toc.txt"
  if ! sudo docker exec -i graft-postgres pg_restore --list < "$WORK/$file" > "$toc"; then
    bad "archive table of contents is unreadable"
    return 1
  fi
  entries=$(grep -vc '^;' "$toc" || true)
  if [ "$entries" -lt 1 ]; then
    bad "archive contains no objects"
    return 1
  fi
  ok "table of contents readable: $entries objects"

  layer 3 "Restore into a throwaway database"
  local image
  if ! image=$(sudo docker inspect -f '{{.Config.Image}}' graft-postgres); then
    bad "graft-postgres container not found"
    return 1
  fi
  CNAME="graft-restore-test-$$"
  log "   starting $image (isolated: no network, deleted afterwards)"
  if ! sudo docker run -d --name "$CNAME" --network none -e POSTGRES_PASSWORD=restoretest \
    -v "$WORK:/restore:ro" "$image" >/dev/null; then
    bad "could not start the throwaway database"
    return 1
  fi
  # initdb runs a temporary server first, so wait for the second "ready" line.
  for i in $(seq 1 120); do
    if [ "$(sudo docker logs "$CNAME" 2>&1 | grep -c 'ready to accept connections' || true)" -ge 2 ]; then
      break
    fi
    sleep 1
  done
  if [ "$i" -ge 120 ]; then
    bad "throwaway database did not become ready within 120s"
    return 1
  fi
  if ! sudo docker exec "$CNAME" createdb -U postgres restore_test; then
    bad "could not create the test database"
    return 1
  fi
  if ! sudo docker exec "$CNAME" pg_restore -U postgres -d restore_test --no-owner --no-acl --exit-on-error \
    "/restore/$file" 2> "$WORK/restore.err"; then
    bad "pg_restore failed - this backup is NOT restorable: $(head -n 3 "$WORK/restore.err" | tr '\n' ' ')"
    return 1
  fi
  ok "pg_restore completed with zero errors (strict mode)"

  layer 4 "Schema comparison against the live database"
  local rc="$WORK/counts.restored" lc="$WORK/counts.live" have_live=1
  test_psql "$COUNTS_SQL" > "$rc" || { bad "could not read table counts from the restored copy"; return 1; }
  if ! live_psql "$COUNTS_SQL" > "$lc"; then
    have_live=0
    : > "$lc"
    warn "live database '$DB' is not readable - skipping live comparisons"
  fi
  local rtables ltables
  rtables=$(grep -c . "$rc" || true)
  ltables=$(grep -c . "$lc" || true)
  log "   restored copy: $rtables tables; live database: $ltables tables"
  if [ "$rtables" -eq 0 ] && [ "$ltables" -gt 0 ]; then
    bad "backup has NO tables but the live database has $ltables - this backup is empty"
    return 1
  fi
  ok "backup contains $rtables tables"

  layer 5 "Row counts per table (backup vs live)"
  local joined="$WORK/counts.joined" t r l lost=0 added=0 rtotal=0 ltotal=0
  awk -F'\t' 'NR==FNR{r[$1]=$2; next} {l[$1]=$2}
    END{for(k in r) a[k]=1; for(k in l) a[k]=1
        for(k in a) print k "\t" ((k in r)?r[k]:"-") "\t" ((k in l)?l[k]:"-")}' "$rc" "$lc" | sort > "$joined"
  while IFS=$'\t' read -r t r l; do
    if [ "$r" = "-" ]; then
      warn "$t exists live ($l rows) but not in this backup - created after the backup was taken"
      ltotal=$((ltotal + l))
    elif [ "$l" = "-" ]; then
      if [ "$have_live" = 1 ]; then
        warn "$t is in the backup ($r rows) but not in the live database - dropped since"
      fi
      rtotal=$((rtotal + r))
    else
      rtotal=$((rtotal + r))
      ltotal=$((ltotal + l))
      if [ "$r" -eq "$l" ]; then
        ok "$t: $r rows (identical to live)"
      elif [ "$r" -lt "$l" ]; then
        lost=$((lost + l - r))
        log "   ℹ $t: backup $r rows, live $l rows (+$((l - r)) written since the backup)"
      else
        added=$((added + r - l))
        warn "$t: backup has $r rows but live has only $l - $((r - l)) rows were deleted since the backup"
      fi
    fi
  done < "$joined"
  log "   totals: backup $rtotal rows, live $ltotal rows"
  if [ "$have_live" = 1 ] && [ "$lost" -gt 0 ]; then
    warn "restoring this backup would discard about $lost row(s) written after it was taken"
  fi

  layer 6 "Freshness: how current is this backup?"
  local age_s newer latest interval_h count
  age_s=$(( $(date +%s) - $(file_epoch "$file") ))
  log "   taken $((age_s / 3600))h $(((age_s % 3600) / 60))m ago"
  latest=$(latest_backup)
  newer=$(list_backups | awk -F'\t' -v f="$file" '$1 > f' | grep -c . || true)
  if [ "$newer" -gt 0 ]; then
    warn "$newer newer backup(s) exist - the latest is $latest"
  else
    ok "this is the newest backup"
  fi
  interval_h=24
  if [ -n "$SCHED_HOURS" ]; then
    count=$(echo "$SCHED_HOURS" | tr ',' '\n' | grep -c . || true)
    if [ "$count" -ge 1 ]; then
      interval_h=$((24 / count))
    fi
  fi
  if [ "$age_s" -gt $((interval_h * 3600 * 2)) ]; then
    warn "older than two backup intervals (${interval_h}h each) - data from the last $((age_s / 3600))h is not in it"
  else
    ok "within two backup intervals (${interval_h}h each)"
  fi
  if test_psql "$STAMPS_SQL" > "$WORK/stamps.restored" && live_psql "$STAMPS_SQL" > "$WORK/stamps.live" 2>/dev/null; then
    local k rv lv
    while IFS=$'\t' read -r k rv; do
      lv=$(awk -F'\t' -v k="$k" '$1 == k {print $2}' "$WORK/stamps.live")
      log "   ℹ newest $k: backup ${rv:-none}, live ${lv:-none}"
    done < "$WORK/stamps.restored"
  fi

  layer 7 "Post-restore health checks"
  local invalid
  invalid=$(test_psql "SELECT count(*) FROM pg_index WHERE NOT indisvalid" || echo "?")
  if [ "$invalid" = "0" ]; then
    ok "all indexes valid"
  else
    bad "invalid indexes found in the restored copy: $invalid"
  fi
  if test_psql "ANALYZE" >/dev/null 2>&1; then
    ok "ANALYZE ran cleanly over every table"
  else
    warn "ANALYZE reported a problem"
  fi

  log "── Verification summary for $file: $PASS passed, $WARN warning(s), $BAD failed"
  if [ "$BAD" -gt 0 ]; then
    log "❌ NOT SAFE: this backup failed verification."
    return 1
  fi
  echo "$file $(date -u +%FT%TZ)" >> "$VERIFIED"
  { echo "$file"; cat "$rc"; } > "$LASTCOUNTS"
  if [ "$WARN" -gt 0 ]; then
    log "✅ Restorable, with $WARN warning(s) above - read them before restoring."
  else
    log "✅ Restorable and consistent with the live database."
  fi
  return 0
}

do_test_restore() {
  local file rc=0
  file=$(pick_backup "${1:-}")
  verify_backup "$file" || rc=$?
  if [ "$rc" -eq 0 ] && [ "${2:-}" = "inspect" ] && [ -t 0 ]; then
    log "🔎 Opening psql on the restored copy. Type \\q to leave - the test database is deleted when you do."
    sudo docker exec -it "$CNAME" psql -U postgres -d restore_test || true
  fi
  drop_testdb
  return "$rc"
}

# A passing verification stamp is only trusted for 30 minutes.
recently_verified() {
  local ts
  ts=$(grep "^$1 " "$VERIFIED" 2>/dev/null | tail -n 1 | cut -d' ' -f2 || true)
  if [ -z "$ts" ]; then
    return 1
  fi
  [ $(( $(date +%s) - $(date -d "$ts" +%s) )) -le 1800 ]
}

# Restores a backup over the live database. The caller (graft CLI) has already
# asked the user to confirm. Safeguards, in order: the backup must have passed
# verification in the last 30 minutes (it is re-verified here otherwise), a
# safety backup of the current data is taken, the restore itself is one
# all-or-nothing transaction, and the result is compared with what was verified.
do_restore() {
  mkwork
  local file
  file=$(pick_backup "${1:-}")
  log "══ Restore of $file into '$DB' ══"

  if recently_verified "$file"; then
    log "🛡️  $file passed verification within the last 30 minutes - reusing that result."
  else
    log "🛡️  $file has not been verified recently - verifying now before touching '$DB'."
    if ! verify_backup "$file"; then
      drop_testdb
      fail "verification failed - '$DB' was NOT touched"
    fi
    drop_testdb
  fi
  if [ ! -f "$WORK/$file" ]; then
    log "📥 Downloading $file..."
    aws s3 cp "s3://$BUCKET/$PREFIX$file" "/work/$file" --only-show-errors || fail "download of $file failed"
  fi

  log "🛟 Taking a safety backup of the current '$DB' first (so this restore can be undone)..."
  do_run
  log "♻️  Restoring $file into '$DB' (single transaction: any error rolls everything back)..."
  sudo docker exec -i graft-postgres pg_restore -U "$PGU" -d "$DB" --clean --if-exists \
    --single-transaction --exit-on-error < "$WORK/$file" \
    || fail "restore failed and was rolled back - '$DB' is unchanged (safety backup kept in the bucket)"

  log "🔍 Checking the live database against the verified copy..."
  if [ -f "$LASTCOUNTS" ] && [ "$(head -n 1 "$LASTCOUNTS")" = "$file" ]; then
    tail -n +2 "$LASTCOUNTS" > "$WORK/counts.expected"
    live_psql "$COUNTS_SQL" > "$WORK/counts.after"
    if cmp -s "$WORK/counts.expected" "$WORK/counts.after"; then
      log "   ✔ every table's row count matches the verified copy"
    else
      log "   ⚠ row counts differ from the verified copy (writes may have landed during the restore):"
      diff "$WORK/counts.expected" "$WORK/counts.after" | sed 's/^/     /' | tee -a "$LOGF" || true
    fi
  else
    log "   (no saved verification counts for this file; skipped)"
  fi
  log "✅ '$DB' restored from $file."
  notify "♻️ graft: database '$DB' on $(hostname) was restored from $file."
}

case "$ACTION" in
  run)
    do_run
    do_prune
    ;;
  scheduled)
    # Cron fires hourly; only run on the hours chosen in "backup set", evaluated
    # in the chosen time zone so daylight saving shifts are followed.
    hour=$(TZ="$SCHED_TZ" date +%-H)
    case ",$SCHED_HOURS," in
      *",$hour,"*) ;;
      *)
        stale_check
        exit 0
        ;;
    esac
    ALERT=1
    log "⏰ scheduled backup of $DB"
    do_run
    do_prune
    ;;
  prune) do_prune ;;
  keep-only)
    ALERT=1
    do_keep_only "${1:-}"
    ;;
  alert-test)
    if [ -z "$TG_TOKEN" ] || [ -z "$TG_CHAT" ]; then
      fail "no Telegram bot is configured - run: graft db $DB backup alert"
    fi
    notify "✅ graft: Telegram alerts for database '$DB' on $(hostname) are working."
    ;;
  check) do_check ;;
  info) do_info ;;
  plain) do_plain ;;
  log) tail -n "${1:-120}" "$LOGF" 2>/dev/null || echo "no log yet" ;;
  presign) do_presign "${1:-}" "${2:-3600}" ;;
  test-restore) do_test_restore "${1:-}" "${2:-}" ;;
  restore)
    ALERT=1
    do_restore "${1:-}"
    ;;
  *)
    echo "unknown action '$ACTION'" >&2
    exit 2
    ;;
esac
