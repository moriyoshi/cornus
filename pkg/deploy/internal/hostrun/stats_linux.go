//go:build linux

package hostrun

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"strings"
)

// ReadNetDev parses the /proc/<pid>/net/dev-style file at path into docker
// per-interface counters. Best-effort: any error yields nil and the stats
// frame simply omits networks.
func ReadNetDev(path string) map[string]DockerNetStats {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	return parseNetDev(f)
}

// parseNetDev parses /proc/net/dev content: two header lines, then one line
// per interface carrying 16 counters (8 receive, 8 transmit). The loopback
// interface is excluded, docker parity.
func parseNetDev(r io.Reader) map[string]DockerNetStats {
	out := map[string]DockerNetStats{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue // header lines
		}
		name = strings.TrimSpace(name)
		if name == "" || name == "lo" {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 16 {
			continue
		}
		n := make([]uint64, 16)
		for i := range n {
			n[i], _ = strconv.ParseUint(fields[i], 10, 64)
		}
		out[name] = DockerNetStats{
			RxBytes:   n[0],
			RxPackets: n[1],
			RxErrors:  n[2],
			RxDropped: n[3],
			TxBytes:   n[8],
			TxPackets: n[9],
			TxErrors:  n[10],
			TxDropped: n[11],
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SystemCPUUsage reads the host's cumulative CPU time from /proc/stat in
// nanoseconds (docker's system_cpu_usage semantics; jiffies at USER_HZ=100).
func SystemCPUUsage() uint64 {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 || fields[0] != "cpu" {
			continue
		}
		var total uint64
		for _, v := range fields[1:] {
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				continue
			}
			total += n
		}
		return total * (1e9 / 100)
	}
	return 0
}

// HostMemTotal reads MemTotal from /proc/meminfo in bytes.
func HostMemTotal() uint64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0
			}
			return kb * 1024
		}
	}
	return 0
}
