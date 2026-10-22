# Movement catalogue

A movement moves a quantity of one (SKU, lot) from one location and state to another. There are twelve kinds. The list is `AllMovementKinds` in `internal/inventory/types.go`.

| Kind | From | To | Trigger |
|---|---|---|---|
| `receive` | `SUPPLIER` | receiving location | ASN line scanned |
| `putaway` | receiving location | storage location | putaway task confirmed |
| `reserve` | location (`available`) | same location (`reserved`) | order allocated |
| `release` | location (`reserved`) | same location (`available`) | order cancelled or reallocated |
| `pick` | storage location (`reserved`) | pick cart LPN (`in_transit`) | pick confirmed |
| `pack` | pick cart LPN | packing station | pack confirmed |
| `ship` | packing station | `CARRIER` | carrier handoff scanned |
| `count_adjust` | location or `VARIANCE` | `VARIANCE` or location | variance confirmed |
| `quarantine` | location (`available`) | same location (`quarantined`) | hold applied |
| `return_receive` | `RETURN_INTAKE` | returns location | return scanned |
| `disposition` | returns location | storage, quarantine, or `SCRAP` | disposition decided |
| `relocate` | location | location | slotting move confirmed |

A state change inside one location is still a movement. This keeps the model uniform and keeps the reserved and available ledger auditable.

## The seven invariants

These are tests. They run against a real PostgreSQL on every push, and every workflow test ends by checking them.

1. **Conservation.** The sum of all positions across real and virtual locations is zero.
2. **Position identity.** For every (SKU, lot, location, LPN), on hand equals the sum of the five states, and every term is non-negative at a real location.
3. **Projection fidelity.** `rebuild positions` produces a table identical to the live one.
4. **Reservation bound.** Reserved quantity never exceeds what was available when it was reserved. Concurrent attempts on one available unit produce exactly one success.
5. **Release exactness.** Cancelling an allocation releases exactly the reserved quantity, exactly once, however often the cancel is retried.
6. **Movement immutability.** The database refuses `UPDATE` and `DELETE` on movements.
7. **Task and movement coupling.** Every `pick`, `putaway`, `count_adjust` and `disposition` movement references a completed task, and every completed task of those kinds has its movements.

## Rebuild

```bash
crossdock rebuild positions
```

This rebuilds a warehouse's positions table from the movement log.
