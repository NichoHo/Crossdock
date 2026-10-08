"""Tests for the slotting solver. No database needed:  python -m unittest test_slotting"""
import unittest

import slotting as sl


def line_instance(velocities, start=None, caps=None, pairs=None):
    """Locations along aisle 1 at bays 1..n (closer to the dock = lower bay).
    SKU i starts in location start[i], default the reverse of their popularity."""
    n = len(velocities)
    locs = [f"A01-{b:02d}-A-1" for b in range(1, n + 1)]
    return sl.Instance(
        locations=locs, capacity=caps or [100] * n,
        skus=[f"S{i}" for i in range(n)], units=[10] * n,
        current=start if start is not None else list(range(n)),
        velocity=list(velocities), pairs=pairs or {})


class Geometry(unittest.TestCase):
    def test_matches_the_router(self):
        # internal/outbound/route.go: same aisle |bay diff| * 1.2 m, else via the cross aisle.
        self.assertEqual(sl.dist((1, 5), (1, 9)), 4 * 12)
        self.assertEqual(sl.dist((1, 5), (3, 2)), 5 * 12 + 2 * 12 + 2 * 30)
        self.assertEqual(sl.dist(sl.DOCK, (2, 4)), 4 * 12 + 2 * 30)
        self.assertEqual(sl.parse("A07-12-C-2"), (7, 12))


class Solver(unittest.TestCase):
    def test_fast_movers_move_toward_the_dock(self):
        # The fastest SKU starts farthest from the dock.
        inst = line_instance([300, 80, 20, 5, 1], start=[4, 3, 2, 1, 0])
        assign, solver, status, before, after = sl.propose(inst, max_moves=10, time_limit=5)
        self.assertEqual(solver, "cpsat")
        self.assertLess(after, before)
        order = sorted(range(5), key=lambda s: assign[s])
        self.assertEqual(order, [0, 1, 2, 3, 4], "nearest slot should hold the fastest SKU")

    def test_never_exceeds_the_move_budget(self):
        inst = line_instance([300, 80, 20, 5, 1], start=[4, 3, 2, 1, 0])
        for budget in (0, 1, 2, 3, 4):
            assign, *_ = sl.propose(inst, max_moves=budget, time_limit=5)
            self.assertLessEqual(len(sl.moves_of(inst, assign)), budget, f"budget {budget}")

    def test_a_good_layout_is_left_alone(self):
        inst = line_instance([300, 80, 20, 5, 1], start=[0, 1, 2, 3, 4])
        assign, *_ , before, after = sl.propose(inst, max_moves=10, time_limit=5)
        self.assertEqual(sl.moves_of(inst, assign), [])
        self.assertEqual(before, after)

    def test_never_worse_than_doing_nothing(self):
        inst = line_instance([7, 7, 3, 3, 9, 1, 4], start=[3, 0, 6, 1, 5, 2, 4])
        assign, _, _, before, after = sl.propose(inst, max_moves=4, time_limit=5)
        self.assertLessEqual(after, before)
        self.assertEqual(after, sl.objective(inst, assign))

    def test_respects_location_capacity(self):
        # S0 is the fastest but needs 50 units; only the far location holds that many.
        inst = line_instance([300, 80, 20], start=[2, 1, 0], caps=[20, 20, 60])
        inst.units = [50, 10, 10]
        assign, *_ = sl.propose(inst, max_moves=10, time_limit=5)
        self.assertEqual(assign[0], 2, "S0 cannot leave the only location big enough")

    def test_each_location_holds_one_sku(self):
        inst = line_instance([50, 40, 30, 20, 10, 5], start=[5, 4, 3, 2, 1, 0])
        assign, *_ = sl.propose(inst, max_moves=10, time_limit=5)
        self.assertEqual(len(set(assign)), len(assign))

    def test_affinity_pulls_pairs_together(self):
        # Equal velocity; S0 and S5 are always ordered together but start at opposite ends.
        inst = line_instance([10, 10, 10, 10, 10, 10], start=[0, 1, 2, 3, 4, 5], pairs={(0, 5): 200})
        assign, *_ = sl.propose(inst, max_moves=2, time_limit=5)
        self.assertLess(abs(assign[0] - assign[5]), 5, "the pair should end up closer than the 5 bays they started at")


class Greedy(unittest.TestCase):
    def test_improves_and_stays_inside_the_budget(self):
        inst = line_instance([300, 80, 20, 5, 1], start=[4, 3, 2, 1, 0])
        assign, status, obj = sl.solve_greedy(inst, max_moves=2)
        self.assertEqual(status, "GREEDY")
        self.assertLess(obj, sl.objective(inst, inst.current))
        self.assertLessEqual(len(sl.moves_of(inst, assign)), 2)
        self.assertEqual(len(set(assign)), len(assign))

    def test_falls_back_when_the_solver_gives_up(self):
        inst = line_instance([300, 80, 20, 5, 1], start=[4, 3, 2, 1, 0])
        original = sl.solve_cpsat
        sl.solve_cpsat = lambda *a, **k: None
        try:
            _, solver, *_ = sl.propose(inst, max_moves=4, time_limit=1)
        finally:
            sl.solve_cpsat = original
        self.assertEqual(solver, "greedy")


if __name__ == "__main__":
    unittest.main()
