package webbff

import "golang.org/x/sys/unix"

// pseudoFilesystems are the kernel filesystems the explorer must never expose. Each is
// a control or introspection surface, not storage: browsing one leaks process state, and
// writing one reconfigures the running kernel.
//
// tmpfs is deliberately ABSENT. It would be a tempting entry — /dev is devtmpfs, which
// reports TMPFS_MAGIC — but tmpfs is an ordinary writable filesystem that projects
// legitimately bind-mount, so refusing it by type would reject real configurations. What
// makes /dev dangerous is its device NODES, and those are refused by file type wherever
// they appear, which is both narrower and more complete than refusing the filesystem
// (a device node in an ordinary directory is caught too).
// x/sys/unix does not export these two, so they are spelled out. Both are stable
// kernel ABI (linux/magic.h) and have been since they were introduced.
const (
	mqueueMagic   = 0x19800202
	configfsMagic = 0x62656570
)

var pseudoFilesystems = map[int64]string{
	unix.PROC_SUPER_MAGIC:    "/proc",
	unix.SYSFS_MAGIC:         "sysfs",
	unix.DEBUGFS_MAGIC:       "debugfs",
	unix.TRACEFS_MAGIC:       "tracefs",
	unix.BPF_FS_MAGIC:        "bpffs",
	unix.CGROUP_SUPER_MAGIC:  "cgroup",
	unix.CGROUP2_SUPER_MAGIC: "cgroup2",
	unix.SECURITYFS_MAGIC:    "securityfs",
	unix.SELINUX_MAGIC:       "selinuxfs",
	unix.PSTOREFS_MAGIC:      "pstore",
	unix.EFIVARFS_MAGIC:      "efivarfs",
	unix.HUGETLBFS_MAGIC:     "hugetlbfs",
	mqueueMagic:              "mqueue",
	unix.BINFMTFS_MAGIC:      "binfmt_misc",
	configfsMagic:            "configfs",
}

// pseudoFilesystem reports whether dir lives on one of them, naming it when so. A statfs
// that fails is treated as NOT pseudo: the directory is then almost certainly
// unreachable, and the callers' own stat will produce a better error than a guess here.
func pseudoFilesystem(dir string) (name string, ok bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return "", false
	}
	name, ok = pseudoFilesystems[int64(st.Type)]
	return name, ok
}
