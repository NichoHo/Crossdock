"""Slotting job (spec 10): which SKU belongs in which pick-face location.

A batch job, not a service. It reads pick history from the database, finds a
better assignment of SKUs to pick-face locations with a constraint solver
(OR-Tools CP-SAT, greedy fallback), and writes a proposal. It has no API.

The solver optimises a proxy: how often each SKU is visited times its distance
from the dock, plus how far apart SKUs that are ordered together end up. That
number is NOT shown to supervisors as the saving. The platform replays the
historical waves through its own router against both layouts (crossdock
slotting replay) and shows that figure instead.

    python slotting.py --warehouse wh-demo [--window-days 90] [--max-moves 20]
"""
from __future__ import annotations

import argparse
import itertools
import json
import os
import subprocess
import sys
import uuid
from collections import Counter, defaultdict
from dataclasses import dataclass, field

# Floor geometry. Must match internal/outbound/route.go so the solver's idea of
# distance is the router's (in decimetres here, to stay integral for CP-SAT).
AISLE_DM = 30   # 3.0 m between aisle centre lines
BAY_DM = 12     # 1.2 m per bay


def parse(code: str) -> tuple[int, int]:
    """A07-12-C-2 -> (aisle 7, bay 12)."""
    return int(code[1:3]), int(code[4:6])


def dist(a: tuple[int, int], b: tuple[int, int]) -> int:
    """Walking distance in decimetres: along the aisle, or via the cross aisle at bay 0."""
    if a[0] == b[0]:
        return abs(a[1] - b[1]) * BAY_DM
    return a[1] * BAY_DM + b[1] * BAY_DM + abs(a[0] - b[0]) * AISLE_DM


DOCK = (0, 0)


@dataclass
class Instance:
    """Everything the solver needs, with no database in it."""
    locations: list[str]                  # candidate pick-face locations
    capacity: list[int]                   # max units per location
    skus: list[str]
    units: list[int]                      # units of each SKU that must fit where it goes
    current: list[int]                    # index into locations
    velocity: list[int]                   # pick lines in the window
    pairs: dict[tuple[int, int], int] = field(default_factory=dict)   # (i, j) i<j -> orders containing both
    # Locations a SKU may not go to (occupied by a SKU we cannot move). Already removed from `locations`.

    def allowed(self, s: int) -> list[int]:
        return [l for l in range(len(self.locations)) if self.capacity[l] >= self.units[s] or l == self.current[s]]

    def point(self, l: int) -> tuple[int, int]:
        return parse(self.locations[l])


def objective(inst: Instance, assign: list[int]) -> int:
    """The solver's proxy cost for an assignment (lower is better)."""
    total = 0
    for s, l in enumerate(assign):
        total += 2 * inst.velocity[s] * dist(DOCK, inst.point(l))
    for (i, j), w in inst.pairs.items():
        total += w * dist(inst.point(assign[i]), inst.point(assign[j]))
    return total


def moves_of(inst: Instance, assign: list[int]) -> list[tuple[int, int, int]]:
    """(sku index, from location, to location) for every SKU that moves, fastest movers first."""
    out = [(s, inst.current[s], l) for s, l in enumerate(assign) if l != inst.current[s]]
    out.sort(key=lambda m: -inst.velocity[m[0]])
    return out


def solve_cpsat(inst: Instance, max_moves: int, time_limit: float, workers: int = 8):
    """Returns (assignment, status name, objective) or None when no solution was found in time."""
    from ortools.sat.python import cp_model

    L, n = len(inst.locations), len(inst.skus)
    m = cp_model.CpModel()
    dock_table = [dist(DOCK, inst.point(l)) for l in range(L)]
    aisle_table = [inst.point(l)[0] for l in range(L)]
    bay_table = [inst.point(l)[1] for l in range(L)]
    max_aisle, max_bay = max(aisle_table), max(bay_table)

    x = [m.NewIntVarFromDomain(cp_model.Domain.FromValues(inst.allowed(s)), f"x{s}") for s in range(n)]
    m.AddAllDifferent(x)
    moved = []
    for s in range(n):
        b = m.NewBoolVar(f"moved{s}")
        m.Add(x[s] != inst.current[s]).OnlyEnforceIf(b)
        m.Add(x[s] == inst.current[s]).OnlyEnforceIf(b.Not())
        moved.append(b)
    m.Add(sum(moved) <= max_moves)

    terms = []
    for s in range(n):
        d = m.NewIntVar(0, max(dock_table), f"d{s}")
        m.AddElement(x[s], dock_table, d)
        terms.append(2 * inst.velocity[s] * d)

    # Pair distance from coordinates, not a table over every pair of locations
    # (that table is L*L entries per pair and was too big to even presolve).
    coords = {}
    def coord(s):
        if s not in coords:
            a_ = m.NewIntVar(0, max_aisle, f"a{s}")
            b_ = m.NewIntVar(0, max_bay, f"b{s}")
            m.AddElement(x[s], aisle_table, a_)
            m.AddElement(x[s], bay_table, b_)
            coords[s] = (a_, b_)
        return coords[s]
    for (i, j), w in inst.pairs.items():
        ai, bi = coord(i)
        aj, bj = coord(j)
        same = m.NewBoolVar(f"same{i}_{j}")
        m.Add(ai == aj).OnlyEnforceIf(same)
        m.Add(ai != aj).OnlyEnforceIf(same.Not())
        da = m.NewIntVar(0, max_aisle, f"da{i}_{j}")
        db = m.NewIntVar(0, max_bay, f"db{i}_{j}")
        m.AddAbsEquality(da, ai - aj)
        m.AddAbsEquality(db, bi - bj)
        dp = m.NewIntVar(0, 2 * max_bay * BAY_DM + max_aisle * AISLE_DM, f"p{i}_{j}")
        m.Add(dp == BAY_DM * db).OnlyEnforceIf(same)
        m.Add(dp == BAY_DM * (bi + bj) + AISLE_DM * da).OnlyEnforceIf(same.Not())
        terms.append(w * dp)
    m.Minimize(sum(terms))

    # Start from the current layout so there is always a feasible answer to improve on.
    for s in range(n):
        m.AddHint(x[s], inst.current[s])
    solver = cp_model.CpSolver()
    solver.parameters.max_time_in_seconds = time_limit
    solver.parameters.num_workers = workers
    status = solver.Solve(m)
    name = solver.StatusName(status)
    if status not in (cp_model.OPTIMAL, cp_model.FEASIBLE):
        return None
    assign = [solver.Value(v) for v in x]
    return assign, name, objective(inst, assign)


def solve_greedy(inst: Instance, max_moves: int):
    """Fallback when the solver times out: fastest movers take the slots nearest the
    dock, swapping with whoever is there, until the move budget is spent."""
    assign = list(inst.current)
    owner = {l: s for s, l in enumerate(assign)}
    order = sorted(range(len(inst.skus)), key=lambda s: (-inst.velocity[s], inst.skus[s]))
    slots = sorted(range(len(inst.locations)), key=lambda l: (dist(DOCK, inst.point(l)), inst.locations[l]))
    used = 0
    for s in order:
        for l in slots:
            if dist(DOCK, inst.point(l)) >= dist(DOCK, inst.point(assign[s])):
                break  # nothing nearer than where it already is
            if l not in inst.allowed(s):
                continue
            other = owner.get(l)
            cost = 1 if other is None else 2
            if other is not None and (inst.velocity[other] >= inst.velocity[s] or assign[s] not in inst.allowed(other)):
                continue
            if used + cost > max_moves:
                continue
            old = assign[s]
            assign[s] = l
            owner[l] = s
            if other is None:
                owner.pop(old, None)
            else:
                assign[other] = old
                owner[old] = other
            used += cost
            break
    return assign, "GREEDY", objective(inst, assign)


def propose(inst: Instance, max_moves: int, time_limit: float):
    """Best assignment found. Falls back to greedy if CP-SAT finds nothing in time
    or returns something no better than doing nothing."""
    before = objective(inst, inst.current)
    solved = None
    try:
        solved = solve_cpsat(inst, max_moves, time_limit)
    except ImportError:
        pass  # ortools not installed: greedy only
    solver = "cpsat"
    if solved is None:
        solved, solver = solve_greedy(inst, max_moves), "greedy"
    assign, status, obj = solved
    if obj >= before and solver == "cpsat":
        g_assign, g_status, g_obj = solve_greedy(inst, max_moves)
        if g_obj < obj:
            assign, status, obj, solver = g_assign, g_status, g_obj, "greedy"
    return assign, solver, status, before, obj


# ---------------------------------------------------------------------------
# Database
# ---------------------------------------------------------------------------

def load_instance(conn, wid: str, window_days: int, top_skus: int, top_pairs: int) -> Instance | None:
    cur = conn.cursor()
    cur.execute(
        """SELECT l.code, l.max_units FROM locations l JOIN zones z ON z.id = l.zone_id
           WHERE l.warehouse_id = %s AND z.purpose = 'pick_face' ORDER BY l.code""", (wid,))
    pickface = {code: cap for code, cap in cur.fetchall()}

    cur.execute(
        """SELECT location, sku_id, SUM(available + reserved + quarantined + damaged)
           FROM positions WHERE warehouse_id = %s AND location = ANY(%s)
           GROUP BY location, sku_id HAVING SUM(available + reserved + quarantined + damaged) > 0""",
        (wid, list(pickface)))
    at_location: dict[str, list[tuple[str, int]]] = defaultdict(list)
    where: dict[str, list[tuple[str, int]]] = defaultdict(list)
    for loc, sku, units in cur.fetchall():
        at_location[loc].append((sku, int(units)))
        where[sku].append((loc, int(units)))

    # A SKU can be moved if all its pick-face stock sits in one location that holds nothing else.
    movable = {sku: locs[0] for sku, locs in where.items()
               if len(locs) == 1 and len(at_location[locs[0][0]]) == 1}
    blocked = {loc for loc, items in at_location.items() if any(sku not in movable for sku, _ in items)}

    cur.execute(
        """SELECT sku_id, order_id FROM tasks
           WHERE warehouse_id = %s AND kind = 'pick' AND state = 'completed'
             AND completed_at >= NOW() - (%s || ' days')::interval""", (wid, str(window_days)))
    velocity: Counter[str] = Counter()
    orders: dict[str, set[str]] = defaultdict(set)
    for sku, order_id in cur.fetchall():
        velocity[sku] += 1
        if order_id:
            orders[order_id].add(sku)
    cur.close()

    cands = [s for s in movable if velocity[s] > 0]
    cands.sort(key=lambda s: (-velocity[s], s))
    cands = cands[:top_skus]
    if len(cands) < 2:
        return None
    index = {s: i for i, s in enumerate(cands)}

    locs = [loc for loc in pickface if loc not in blocked]
    lidx = {loc: i for i, loc in enumerate(locs)}
    pair_count: Counter[tuple[int, int]] = Counter()
    for skus in orders.values():
        for a, b in itertools.combinations(sorted(s for s in skus if s in index), 2):
            pair_count[(index[a], index[b])] += 1
    pairs = dict(pair_count.most_common(top_pairs))

    return Instance(
        locations=locs, capacity=[pickface[l] for l in locs],
        skus=cands, units=[movable[s][1] for s in cands],
        current=[lidx[movable[s][0]] for s in cands],
        velocity=[velocity[s] for s in cands], pairs=pairs)


def write_proposal(conn, wid: str, inst: Instance, assign: list[int], solver: str, status: str,
                   before: int, after: int, window_days: int, max_moves: int) -> str:
    pid = "prop-" + uuid.uuid4().hex[:12]
    cur = conn.cursor()
    cur.execute("UPDATE slotting_proposals SET state = 'superseded' WHERE warehouse_id = %s AND state IN ('proposed', 'replayed')", (wid,))
    cur.execute(
        """INSERT INTO slotting_proposals (id, warehouse_id, window_days, max_moves, solver, solver_status, objective_before, objective_after)
           VALUES (%s, %s, %s, %s, %s, %s, %s, %s)""",
        (pid, wid, window_days, max_moves, solver, status, before, after))
    for s, sku in enumerate(inst.skus):
        cur.execute(
            """INSERT INTO slotting_layout (warehouse_id, proposal_id, sku_id, current_location, proposed_location, velocity)
               VALUES (%s, %s, %s, %s, %s, %s)""",
            (wid, pid, sku, inst.locations[inst.current[s]], inst.locations[assign[s]], inst.velocity[s]))
    for seq, (s, a, b) in enumerate(moves_of(inst, assign), start=1):
        cur.execute(
            """INSERT INTO slotting_moves (warehouse_id, proposal_id, seq, sku_id, from_location, to_location, reason)
               VALUES (%s, %s, %s, %s, %s, %s, %s)""",
            (wid, pid, seq, inst.skus[s], inst.locations[a], inst.locations[b],
             f"{inst.velocity[s]} pick lines in {window_days} days"))
    conn.commit()
    cur.close()
    return pid


def main(argv=None) -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--warehouse", required=True)
    ap.add_argument("--dsn", default=os.environ.get("CROSSDOCK_DATABASE_URL"))
    ap.add_argument("--window-days", type=int, default=90)
    ap.add_argument("--max-moves", type=int, default=20)
    ap.add_argument("--top-skus", type=int, default=60)
    ap.add_argument("--top-pairs", type=int, default=40)
    ap.add_argument("--time-limit", type=float, default=20.0, help="solver seconds")
    ap.add_argument("--replay-bin", default=os.environ.get("CROSSDOCK_BIN"),
                    help="path to the crossdock binary; if given, the job runs 'crossdock slotting replay' on its proposal")
    args = ap.parse_args(argv)
    if not args.dsn:
        print("slotting: set CROSSDOCK_DATABASE_URL or pass --dsn", file=sys.stderr)
        return 2

    import psycopg2
    conn = psycopg2.connect(args.dsn)
    try:
        inst = load_instance(conn, args.warehouse, args.window_days, args.top_skus, args.top_pairs)
        if inst is None:
            print("slotting: not enough pick history or movable SKUs to propose anything", file=sys.stderr)
            return 3
        assign, solver, status, before, after = propose(inst, args.max_moves, args.time_limit)
        moves = moves_of(inst, assign)
        if not moves:
            print(json.dumps({"proposal": None, "message": "the layout is already as good as the solver can make it"}))
            return 0
        pid = write_proposal(conn, args.warehouse, inst, assign, solver, status, before, after, args.window_days, args.max_moves)
    finally:
        conn.close()

    print(json.dumps({"proposal": pid, "solver": solver, "status": status, "moves": len(moves),
                      "objective_before": before, "objective_after": after, "skus_considered": len(inst.skus)}))
    if args.replay_bin:
        subprocess.run([args.replay_bin, "slotting", "replay", "-warehouse", args.warehouse, pid], check=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
