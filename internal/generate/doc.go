// Package generate provides the synthetic data generator for Crossdock.
// It produces a plausible warehouse with a layout, a SKU catalogue with a
// velocity distribution, and a history of inbound and outbound movements
// with configurable exception rates.
//
// The generator is deterministic given the same seed, which ensures that
// tests, demos, and load tests are reproducible.
package generate
