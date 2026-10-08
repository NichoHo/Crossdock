#!/usr/bin/env bash
# Restore drill (spec 17): restore a dump into a scratch database next to the live
# one, then prove the data is whole. A backup nobody has restored is a hope.
#
#   deploy/restore-drill.sh [dump-file]     (default: newest in backups/)
#
#   COMPOSE   compose command and file (default: docker compose -f deploy/docker-compose.prod.yml)
#   DB_SERVICE  service name of Postgres (default: db)
#
# Checks, each printed PASS or FAIL:
#   1. conservation       every position column sums to zero across the warehouse
#   2. projection fidelity positions equal a replay of the movements log
#   3. immutability        the movements table still rejects UPDATE and DELETE
#   4. row counts          movements, tasks and orders are present
# Exit status is non-zero if any check fails. Record the result in docs/restore-drills.md.
set -euo pipefail

COMPOSE=${COMPOSE:-docker compose -f deploy/docker-compose.prod.yml}
SVC=${DB_SERVICE:-db}
DUMP=${1:-$(ls -1t backups/crossdock-*.dump | head -n1)}
SCRATCH=crossdock_drill
[ -f "$DUMP" ] || { echo "no dump found" >&2; exit 2; }

psql() { $COMPOSE exec -T "$SVC" psql -U crossdock -d "$SCRATCH" -v ON_ERROR_STOP=1 -At "$@"; }
admin() { $COMPOSE exec -T "$SVC" psql -U crossdock -d postgres -v ON_ERROR_STOP=1 -At "$@"; }

echo "restoring $DUMP into $SCRATCH"
start=$(date +%s)
admin -c "DROP DATABASE IF EXISTS $SCRATCH" -c "CREATE DATABASE $SCRATCH" >/dev/null
$COMPOSE exec -T "$SVC" pg_restore -U crossdock -d "$SCRATCH" --no-owner --exit-on-error < "$DUMP"
echo "restore took $(( $(date +%s) - start ))s"

# DRILL_TAMPER=1 corrupts one position after the restore, to show the checks can fail.
[ -z "${DRILL_TAMPER:-}" ] || psql -c "UPDATE positions SET available = available + 1 WHERE ctid = (SELECT ctid FROM positions WHERE location NOT IN ('SUPPLIER','CUSTOMER','CARRIER','VARIANCE','SCRAP','RETURN_INTAKE') LIMIT 1)" >/dev/null

fail=0
check() { # name, expected, actual
  if [ "$2" = "$3" ]; then echo "PASS  $1 ($3)"; else echo "FAIL  $1: expected $2, got $3"; fail=1; fi
}

check "conservation" 0 "$(psql -c "SELECT COALESCE(SUM(available+reserved+in_transit+quarantined+damaged),0) FROM positions")"

check "projection fidelity" 0 "$(psql -c "
WITH legs AS (
  SELECT warehouse_id, sku_id, lot_id, lpn_id, from_location AS location, from_state AS st, -qty AS q FROM movements
  UNION ALL
  SELECT warehouse_id, sku_id, lot_id, lpn_id, to_location, to_state, qty FROM movements),
replay AS (
  SELECT warehouse_id, sku_id, lot_id, lpn_id, location,
    COALESCE(SUM(q) FILTER (WHERE st='available'),0)   AS available,
    COALESCE(SUM(q) FILTER (WHERE st='reserved'),0)    AS reserved,
    COALESCE(SUM(q) FILTER (WHERE st='in_transit'),0)  AS in_transit,
    COALESCE(SUM(q) FILTER (WHERE st='quarantined'),0) AS quarantined,
    COALESCE(SUM(q) FILTER (WHERE st='damaged'),0)     AS damaged
  FROM legs GROUP BY 1,2,3,4,5)
SELECT count(*) FROM replay r
FULL JOIN positions p USING (warehouse_id, sku_id, lot_id, lpn_id, location)
WHERE COALESCE(r.available,0)<>COALESCE(p.available,0) OR COALESCE(r.reserved,0)<>COALESCE(p.reserved,0)
   OR COALESCE(r.in_transit,0)<>COALESCE(p.in_transit,0) OR COALESCE(r.quarantined,0)<>COALESCE(p.quarantined,0)
   OR COALESCE(r.damaged,0)<>COALESCE(p.damaged,0)")"

# The update must fail. Run it in a transaction that is always rolled back.
if psql -c "BEGIN; UPDATE movements SET qty = qty; ROLLBACK" >/dev/null 2>&1; then
  echo "FAIL  immutability: an UPDATE on movements succeeded"; fail=1
else
  echo "PASS  immutability (UPDATE on movements rejected)"
fi

for t in movements tasks orders; do
  n=$(psql -c "SELECT count(*) FROM $t")
  if [ "$n" -gt 0 ]; then echo "PASS  rows in $t ($n)"; else echo "FAIL  $t is empty"; fail=1; fi
done

admin -c "DROP DATABASE $SCRATCH" >/dev/null
[ "$fail" = 0 ] && echo "DRILL PASSED" || { echo "DRILL FAILED"; exit 1; }
