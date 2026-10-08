package inventory

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// WarehouseID is the unique identifier for a warehouse site.
type WarehouseID string

// SKUID is a stock keeping unit identifier — the code on the product barcode.
type SKUID string

// LotID identifies a batch of one SKU that shares a receipt date and, if
// applicable, an expiry date. Traceability is per lot.
type LotID string

// LPNID is a licence plate number — the barcode on a physical container
// (pallet, tote, carton). Stock moves by LPN on the floor.
type LPNID string

// LocationCode is the physical address of a slot in the warehouse, formatted
// as AISLE-BAY-LEVEL-POSITION (e.g. "A07-12-C-2"). Virtual locations use
// all-caps sentinel values.
type LocationCode string

// Well-known virtual location codes. Virtual locations stand for the outside
// world. They can hold negative balances; that is how conservation works.
const (
	VirtualSupplier     LocationCode = "SUPPLIER"
	VirtualCustomer     LocationCode = "CUSTOMER"
	VirtualCarrier      LocationCode = "CARRIER"
	VirtualVariance     LocationCode = "VARIANCE"
	VirtualScrap        LocationCode = "SCRAP"
	VirtualReturnIntake LocationCode = "RETURN_INTAKE"
)

// allVirtualLocations is the set of virtual location codes for fast lookup.
var allVirtualLocations = map[LocationCode]struct{}{
	VirtualSupplier:     {},
	VirtualCustomer:     {},
	VirtualCarrier:      {},
	VirtualVariance:     {},
	VirtualScrap:        {},
	VirtualReturnIntake: {},
}

// locationCodePattern matches physical location codes: AISLE-BAY-LEVEL-POSITION.
// Examples: A07-12-C-2, B01-03-A-1
var locationCodePattern = regexp.MustCompile(`^[A-Z]\d{2}-\d{2}-[A-Z]-\d+$`)

// IsVirtual reports whether this location is a virtual (outside-world) location.
func (c LocationCode) IsVirtual() bool {
	_, ok := allVirtualLocations[c]
	return ok
}

// Validate returns an error if the location code is neither a valid physical
// code nor a recognised virtual location.
func (c LocationCode) Validate() error {
	if c.IsVirtual() {
		return nil
	}
	if locationCodePattern.MatchString(string(c)) {
		return nil
	}
	return fmt.Errorf("inventory: invalid location code %q: must match AISLE-BAY-LEVEL-POSITION (e.g. A07-12-C-2) or be a virtual location", c)
}

// String implements fmt.Stringer.
func (c LocationCode) String() string { return string(c) }

// StockState represents the lifecycle state of a quantity of stock at a
// position. The states form a set of mutually exclusive sub-quantities that
// sum to on_hand.
type StockState string

const (
	// StateAvailable stock can be reserved for outbound orders.
	StateAvailable StockState = "available"
	// StateReserved stock is committed to a specific order but not yet picked.
	StateReserved StockState = "reserved"
	// StateInTransit stock is on a pick cart moving toward packing.
	StateInTransit StockState = "in_transit"
	// StateQuarantined stock is held for inspection and cannot be used.
	StateQuarantined StockState = "quarantined"
	// StateDamaged stock is unusable and recorded for variance tracking.
	StateDamaged StockState = "damaged"
)

// AllStates is the exhaustive list of stock states. The sum of quantities in
// these states at a position equals on_hand.
var AllStates = []StockState{
	StateAvailable,
	StateReserved,
	StateInTransit,
	StateQuarantined,
	StateDamaged,
}

// Validate returns an error if the state is not a recognised value.
func (s StockState) Validate() error {
	for _, valid := range AllStates {
		if s == valid {
			return nil
		}
	}
	return fmt.Errorf("inventory: unknown stock state %q", s)
}

// MovementKind describes the business operation that caused a stock movement.
type MovementKind string

const (
	KindReceive       MovementKind = "receive"
	KindPutaway       MovementKind = "putaway"
	KindReserve       MovementKind = "reserve"
	KindRelease       MovementKind = "release"
	KindPick          MovementKind = "pick"
	KindPack          MovementKind = "pack"
	KindShip          MovementKind = "ship"
	KindCountAdjust   MovementKind = "count_adjust"
	KindQuarantine    MovementKind = "quarantine"
	KindReturnReceive MovementKind = "return_receive"
	KindDisposition   MovementKind = "disposition"
	KindRelocate      MovementKind = "relocate"
)

// AllMovementKinds is the exhaustive list of movement kinds.
var AllMovementKinds = []MovementKind{
	KindReceive,
	KindPutaway,
	KindReserve,
	KindRelease,
	KindPick,
	KindPack,
	KindShip,
	KindCountAdjust,
	KindQuarantine,
	KindReturnReceive,
	KindDisposition,
	KindRelocate,
}

// Validate returns an error if the kind is not a recognised value.
func (k MovementKind) Validate() error {
	for _, valid := range AllMovementKinds {
		if k == valid {
			return nil
		}
	}
	return fmt.Errorf("inventory: unknown movement kind %q", k)
}

// ZonePurpose classifies what a zone is for.
type ZonePurpose string

const (
	ZoneReceiving  ZonePurpose = "receiving"
	ZoneBulk       ZonePurpose = "bulk"
	ZonePickFace   ZonePurpose = "pick_face"
	ZonePacking    ZonePurpose = "packing"
	ZoneReturns    ZonePurpose = "returns"
	ZoneQuarantine ZonePurpose = "quarantine"
)

// TaskState is the lifecycle state of an operator task.
type TaskState string

const (
	TaskOpen       TaskState = "open"
	TaskAssigned   TaskState = "assigned"
	TaskInProgress TaskState = "in_progress"
	TaskCompleted  TaskState = "completed"
	TaskCancelled  TaskState = "cancelled"
	TaskException  TaskState = "exception"
)

// TaskKind identifies what kind of work a task involves.
type TaskKind string

const (
	TaskPutaway       TaskKind = "putaway"
	TaskPick          TaskKind = "pick"
	TaskPack          TaskKind = "pack"
	TaskCount         TaskKind = "count"
	TaskReturnIntake  TaskKind = "return_intake"
	TaskDisposition   TaskKind = "disposition"
	TaskRelocate      TaskKind = "relocate"
)

// Warehouse is a physical site. The platform supports several.
type Warehouse struct {
	ID   WarehouseID
	Name string
	// TimeZone is the IANA timezone of the site, used for scheduling.
	TimeZone string
}

// Zone is a region of a warehouse with a purpose.
type Zone struct {
	ID          string
	WarehouseID WarehouseID
	Name        string
	Purpose     ZonePurpose
}

// Location is an addressable slot. Belongs to one zone, has a capacity in
// units (MaxUnits) and optionally in volume (MaxVolumeCM3).
type Location struct {
	Code        LocationCode
	WarehouseID WarehouseID
	ZoneID      string
	// MaxUnits is the unit-count capacity. 0 means unlimited (virtual locations).
	MaxUnits int
	// MaxVolumeCM3 is the volumetric capacity in cubic centimetres. 0 means unlimited.
	MaxVolumeCM3 int64
}

// SKU is a stock keeping unit — a distinct product with a barcode.
type SKU struct {
	ID          SKUID
	WarehouseID WarehouseID
	// Barcode is the primary product barcode (EAN-13, UPC-A, or similar).
	Barcode string
	Name    string
	// WeightG is the unit weight in grams.
	WeightG int
	// VolumeCM3 is the unit volume in cubic centimetres.
	VolumeCM3 int64
	// IsLotTracked indicates whether this SKU requires lot assignment on receipt.
	IsLotTracked bool
	// VelocityClass is the ABC velocity classification (A = fast mover).
	VelocityClass string
}

// Lot is a batch of one SKU sharing a receipt date and, if applicable, an expiry.
type Lot struct {
	ID          LotID
	SKUID       SKUID
	WarehouseID WarehouseID
	ReceivedAt  time.Time
	// ExpiresAt is zero for non-perishable stock.
	ExpiresAt time.Time
}

// LPN is a physical container (pallet, tote, carton) identified by its barcode.
type LPN struct {
	ID          LPNID
	WarehouseID WarehouseID
	// CurrentLocation is where the LPN currently resides. Updated by movements.
	CurrentLocation LocationCode
}

// PositionKey is the composite key that uniquely identifies a position record.
// A position holds the quantity of one (SKU, lot) at one location, optionally
// inside one LPN, split by state.
type PositionKey struct {
	WarehouseID WarehouseID
	SKU         SKUID
	Lot         LotID
	Location    LocationCode
	LPN         LPNID // empty string means not in an LPN
}

// Position is the current quantity of one (SKU, lot) at one location, optionally
// inside one LPN, split by state. It is a projection of the movements log.
//
// The identity invariant requires that for real locations, every quantity is
// non-negative, and their sum equals OnHand.
type Position struct {
	PositionKey
	Available   int64
	Reserved    int64
	InTransit   int64
	Quarantined int64
	Damaged     int64
}

// OnHand returns the total quantity at this position across all states.
func (p Position) OnHand() int64 {
	return p.Available + p.Reserved + p.InTransit + p.Quarantined + p.Damaged
}

// Movement is one double-entry event: a quantity of one (SKU, lot) leaves one
// location and arrives at another, with a state on each side. Movements are
// immutable once written. Corrections are new movements in the opposite direction.
type Movement struct {
	// ID is a UUID assigned by the application on creation.
	ID string
	// WarehouseID is the warehouse this movement belongs to.
	WarehouseID WarehouseID
	// Seq is a monotonically increasing sequence number per warehouse. It is
	// assigned by the database (using a sequence) and is the ordering key for
	// the event log.
	Seq int64
	// Kind is the business operation that caused this movement.
	Kind MovementKind

	// From is the source location.
	FromLocation LocationCode
	// FromState is the stock state at the source.
	FromState StockState
	// To is the destination location.
	ToLocation LocationCode
	// ToState is the stock state at the destination.
	ToState StockState

	// SKU being moved.
	SKUID SKUID
	// Lot being moved.
	LotID LotID
	// LPN being moved (may be empty if movement is not LPN-tracked).
	LPNID LPNID

	// Qty is the quantity moved. Must be positive.
	Qty int64

	// OccurredAt is when the physical event happened (scanner timestamp, not
	// DB insert time). Stored alongside CreatedAt for auditability.
	OccurredAt time.Time
	// CreatedAt is the database insertion timestamp (set by DB default).
	CreatedAt time.Time

	// IdempotencyKey is the client-provided key for retried operations. The
	// combination (WarehouseID, IdempotencyKey) is unique.
	IdempotencyKey string

	// References (optional): link the movement to the task or document that
	// caused it.
	TaskID     string
	ReceiptID  string
	OrderID    string
	CountID    string
	ReturnID   string
	ShipmentID string
}

// Validate returns an error if the movement fails basic sanity checks.
// This is not a full invariant check; it is a precondition check before
// writing to the database.
func (m Movement) Validate() error {
	if m.ID == "" {
		return fmt.Errorf("inventory: movement ID is required")
	}
	if m.WarehouseID == "" {
		return fmt.Errorf("inventory: warehouse ID is required")
	}
	if err := m.Kind.Validate(); err != nil {
		return err
	}
	if err := m.FromLocation.Validate(); err != nil {
		return fmt.Errorf("inventory: from_location: %w", err)
	}
	if err := m.ToLocation.Validate(); err != nil {
		return fmt.Errorf("inventory: to_location: %w", err)
	}
	if err := m.FromState.Validate(); err != nil {
		return fmt.Errorf("inventory: from_state: %w", err)
	}
	if err := m.ToState.Validate(); err != nil {
		return fmt.Errorf("inventory: to_state: %w", err)
	}
	if m.SKUID == "" {
		return fmt.Errorf("inventory: SKU ID is required")
	}
	if m.LotID == "" {
		return fmt.Errorf("inventory: lot ID is required")
	}
	if m.Qty <= 0 {
		return fmt.Errorf("inventory: quantity must be positive, got %d", m.Qty)
	}
	if m.OccurredAt.IsZero() {
		return fmt.Errorf("inventory: occurred_at is required")
	}
	return nil
}

// PositionDelta computes how the movement changes the source and destination
// positions. It returns two Position deltas: one for the from-position (negative)
// and one for the to-position (positive).
func (m Movement) PositionDelta() (from, to PositionKey) {
	from = PositionKey{
		WarehouseID: m.WarehouseID,
		SKU:         m.SKUID,
		Lot:         m.LotID,
		Location:    m.FromLocation,
		LPN:         m.LPNID,
	}
	to = PositionKey{
		WarehouseID: m.WarehouseID,
		SKU:         m.SKUID,
		Lot:         m.LotID,
		Location:    m.ToLocation,
		LPN:         m.LPNID,
	}
	return from, to
}

// Warehouse implements fmt.Stringer.
func (w Warehouse) String() string { return fmt.Sprintf("Warehouse(%s)", w.ID) }

// SKU implements fmt.Stringer.
func (s SKU) String() string { return fmt.Sprintf("SKU(%s,%s)", s.ID, s.Name) }

// MovementSummary is a compact representation of a movement for logging.
func (m Movement) Summary() string {
	return fmt.Sprintf(
		"%s %s qty=%d %s(%s)->%s(%s) [%s]",
		m.Kind, m.SKUID, m.Qty,
		m.FromLocation, m.FromState,
		m.ToLocation, m.ToState,
		strings.TrimPrefix(m.ID, ""),
	)
}
