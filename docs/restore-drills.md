# Restore drills

Run `deploy/restore-drill.sh` each month and add a line here.

| Date | Dump | Where | Result | Notes |
|---|---|---|---|---|
| 2026-10-08 | 18.7 MB, 75,153 movements | Developer laptop, Docker Desktop (not the production host) | PASS, restore took 7 s | All four checks passed. With `DRILL_TAMPER=1` (one position changed by hand after the restore) the drill fails on conservation and on projection fidelity, so the checks can fail. |

The first drill on the production host is still to do.
