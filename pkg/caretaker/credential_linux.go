//go:build linux

package caretaker

import "cornus/pkg/netnsbind"

// ensureLocalAddr makes ip bindable inside the pod netns by adding it to the
// loopback interface (idempotent). It is how the aws-imds "well-known" delivery
// binds 169.254.169.254 — the link-local IMDS address a pod does not otherwise
// carry. Requires NET_ADMIN (the kubernetes backend grants it for WellKnown).
//
// The caretaker is already INSIDE the workload's namespace, so it passes the
// empty path: this is the same operation the server performs from outside via
// netnsbind, and sharing it is deliberate. Two implementations of "make the IMDS
// address bindable" would be two things to keep agreeing about which addresses
// are legal and what "already present" means.
func ensureLocalAddr(ip string) error { return netnsbind.EnsureLocalAddr("", ip) }
