// Package apps turns a set of Kubernetes resources into applications.
//
// This is the core abstraction of kubeside: developers think in services, not
// in ReplicaSets. Grouping is a pure function so it can be tested exhaustively
// against fixture clusters without an apiserver.
package apps
