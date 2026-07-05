package server

import (
	"os"
	"strings"
)

// Accessors for the STRUCTURAL environment variables this package reads: URLs,
// hostnames, filesystem paths, and identifiers. Every one of them trims, and
// every consumer in this package must go through the accessor rather than
// calling os.Getenv directly.
//
// # Why trimming is the right default here, and where it stops
//
// These values are pasted into environments by hand, by YAML, and by shell
// heredocs, all of which produce trailing newlines and stray spaces. Untrimmed,
// a value like "redis://db:6379\n" is SET for every predicate that asks
// `!= ""` and MALFORMED for the one consumer that uses it — which is exactly
// the shape of the CORNUS_ADVERTISE_URL defect (see advertise.go): the server
// concluded a multi-replica hub was configured, then failed to construct it,
// with the cause several frames away from the symptom. CORNUS_HUB_REDIS has the
// same three-site split today: two predicates test only for non-emptiness while
// newHubStore dials the value.
//
// It stops at SECRETS. CORNUS_AUTH_TOKEN and CORNUS_JWT_HS256_SECRET are
// deliberately absent from this file and are still read raw. Trimming a
// credential silently authenticates with a value the operator did not configure,
// which trades one quiet wrong-value bug for another and a worse one — a token
// may legitimately end in whitespace, and cornus has no business deciding it did
// not mean it. Whether those should be trimmed, warned about, or left alone is
// an open question recorded in .agents/docs/TODO.md; it is not answered here.
//
// Read per use rather than memoized, for the reason advertise.go gives at
// length: a Server can be constructed before the environment it will run under
// is final, and every one of these costs a single getenv on a path that is
// already doing real work.
func envTrimmed(name string) string { return strings.TrimSpace(os.Getenv(name)) }

// hubRedisURL is the Redis URL backing a multi-replica hub store
// (CORNUS_HUB_REDIS). Empty selects no Redis store.
func hubRedisURL() string { return envTrimmed("CORNUS_HUB_REDIS") }

// hubStore selects the multi-replica hub store backend (CORNUS_HUB_STORE);
// "kube" is the only recognized non-empty value. It trims for a reason its two
// consumers make sharp: one asks `!= ""` and the other `== "kube"`, so an
// untrimmed "kube\n" reads as "a store is configured" to the first and "not
// kube" to the second, and the server quietly falls back to the in-memory
// registry while believing it is clustered.
func hubStore() string { return envTrimmed("CORNUS_HUB_STORE") }

// hubForwardURL is the base URL peer replicas dial to forward a hub delivery to
// this one (CORNUS_HUB_FORWARD_URL).
func hubForwardURL() string { return envTrimmed("CORNUS_HUB_FORWARD_URL") }

// registryMirror is the upstream registry host pulls fall back to
// (CORNUS_REGISTRY_MIRROR).
func registryMirror() string { return envTrimmed("CORNUS_REGISTRY_MIRROR") }

// k8sNamespace is the namespace the kubernetes-backed hub store and deploy
// backend operate in (CORNUS_K8S_NAMESPACE). Whitespace is not merely untidy
// here: a namespace is a DNS-1123 label, in which it is not legal.
func k8sNamespace() string { return envTrimmed("CORNUS_K8S_NAMESPACE") }

// replicaIDFromEnv is this replica's identity (CORNUS_REPLICA_ID), used as the
// owner key for hub registrations and GC leases. It is compared and stored, so a
// stray newline would partition this replica from itself across restarts.
func replicaIDFromEnv() string { return envTrimmed("CORNUS_REPLICA_ID") }

// jwtIssuer is the expected `iss` claim (CORNUS_JWT_ISSUER). It is compared
// against a value parsed out of a token, so untrimmed it matches nothing and
// every token is rejected for a reason the message cannot explain.
func jwtIssuer() string { return envTrimmed("CORNUS_JWT_ISSUER") }

// jwksURL and jwksFile are the two JWKS sources (CORNUS_JWT_JWKS_URL,
// CORNUS_JWT_JWKS_FILE).
func jwksURL() string  { return envTrimmed("CORNUS_JWT_JWKS_URL") }
func jwksFile() string { return envTrimmed("CORNUS_JWT_JWKS_FILE") }
