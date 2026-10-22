# Domain glossary

The words are the words of the trade. They are not simplified.

## Physical structure

| Term | Meaning |
|---|---|
| **Warehouse** | One physical site. The platform supports several. |
| **Zone** | A region of a warehouse with a purpose: receiving, bulk, pick face, packing, returns, quarantine. |
| **Location** | An addressable slot, coded `AISLE-BAY-LEVEL-POSITION`, for example `A07-12-C-2`. It belongs to one zone and has a capacity. |
| **Virtual location** | A location that stands for the outside world: `SUPPLIER`, `CUSTOMER`, `CARRIER`, `VARIANCE`, `SCRAP`, `RETURN_INTAKE`. These can hold a negative balance. That is how conservation works: every movement adds to one place what it takes from another, so the total is always zero. |

## Stock

| Term | Meaning |
|---|---|
| **SKU** | A stock keeping unit. One distinct product with a barcode. |
| **Lot** | A batch of one SKU that shares a receipt date and, if it applies, an expiry. Traceability is per lot. |
| **LPN** | Licence plate number. A physical container (pallet, tote, carton) with its own barcode. The system tracks what each LPN holds. |
| **Position** | The quantity of one (SKU, lot) at one location, optionally inside one LPN, split by state. |
| **State** | `available`, `reserved`, `in_transit`, `quarantined` or `damaged`. A position holds one quantity per state. |

## Movement

Every change to stock is a **movement**: a quantity of one (SKU, lot) that leaves one location and arrives at another, with a state on each side. A movement is immutable once written. To correct one, write a new movement in the other direction. See the [movement catalogue](movements.md).

## Inbound

**Purchase order**, then **ASN** (advance shipping notice: what the supplier says is coming), then **receipt** (what arrived, scanned line by line), then **putaway task** (where it goes, done by an operator).

The gap between ASN and receipt is the first reconciliation the platform does. A line can end as `short_shipped`, `over_shipped`, `putaway_incomplete` or `lot_mismatch`.

## Outbound

**Order**, then **allocation** (reservation of specific positions; this is the serialised decision), then **wave** (a batch of orders released to the floor together, with a pick route), then **pick tasks**, then **pack**, then **shipment** (carrier, tracking number, optional COD amount), then **carrier handoff**.

## Counting

**Cycle count task** (a location, scheduled by velocity class or by exception), then **count** (what the operator saw, blind), then **variance** (system minus counted, so a positive number means stock is missing), then **classification**, then an **adjustment** movement if a supervisor confirms it.

Classes: `timing`, `miscount`, `damaged`, `shrinkage`, `found`, `unresolved`.

## Returns and settlement

**Return authorisation**, then **return receipt**, then **disposition** (restock, quarantine or scrap).

**COD shipment**, then **delivery outcome** (`delivered_collected`, `rejected`, `lost`), then **courier remittance** (a periodic file of what the courier collected and will pay), then **remittance match**, then a **settlement break** if the amounts disagree: `short_remitted`, `unremitted`, `remitted_for_rejected`, `unknown_shipment` or `duplicate_remittance`.

Money is stored in minor units (satang, cents), one currency per deployment.

## Tasks

Everything an operator does is a **task**: putaway, pick, pack, count, return intake, disposition, relocate. A task moves through `open`, `assigned`, `in_progress`, and ends as `completed`, `cancelled` or `exception`. Tasks are what the scanner shows.
