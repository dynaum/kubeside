// Package clusters manages one connection per kubeconfig context.
//
// Each connection owns its informer factory, REST client, permission cache and
// circuit breaker, so an unreachable cluster never blocks another.
package clusters
