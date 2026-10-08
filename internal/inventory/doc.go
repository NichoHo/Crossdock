// Package inventory defines the core domain types for the Crossdock warehouse
// management system. It is the canonical vocabulary of the platform: stock
// keeping units, lots, licence plate numbers, locations, states, movements, and
// positions.
//
// The design follows the double-entry ledger model described in the spec:
// every stock movement debits one location and credits another, conservation
// is an invariant, and the positions table is a projection of the movements log.
package inventory
