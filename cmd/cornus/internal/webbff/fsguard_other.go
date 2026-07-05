//go:build !linux

package webbff

// pseudoFilesystem is Linux-only: the filesystems it screens out (procfs, sysfs, cgroup,
// bpffs, …) are Linux kernel interfaces, and statfs reports a filesystem TYPE only on
// Linux — darwin's Statfs_t carries a name string with different values entirely.
//
// Returning "not pseudo" here is not a hole. Cornus workloads are Linux containers, so
// the client-local bind sources this screens are Linux paths in every deployment that
// can exist; a non-Linux host has no /proc to expose in the first place. The rest of the
// safeguard — the filesystem-root refusal, the read-only gate, and the refusal of
// non-regular files — is platform-independent and still applies.
func pseudoFilesystem(string) (name string, ok bool) { return "", false }
