//go:build linux

package deploywire

import (
	"fmt"
	"os"
	"strings"
)

func canMountLocal() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("deploywire: client-local mounts require root (CAP_SYS_ADMIN) to kernel-9p-mount")
	}
	fs, err := os.ReadFile("/proc/filesystems")
	if err != nil {
		return fmt.Errorf("deploywire: read /proc/filesystems: %w", err)
	}
	if !strings.Contains(string(fs), "9p") {
		return fmt.Errorf("deploywire: 9p filesystem not available (load the kernel module: modprobe 9p 9pnet)")
	}
	return nil
}
