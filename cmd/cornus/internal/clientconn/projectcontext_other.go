//go:build !unix

package clientconn

// untrustedProvenance is a no-op on non-Unix platforms, whose ownership/permission
// model differs; provenance vetting is Unix-only. The other layers of the trust
// model — bounded discovery, the opt-in field gate, and token co-location — still
// apply on every platform.
func untrustedProvenance(path string) string { return "" }
