package hostenv

import "testing"

const selfContainerID = "1f0a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8"

func TestSelfIDsFromMountinfo(t *testing.T) {
	got := selfIDsFromMountinfo(parseMountinfo(sampleMountinfo))
	if len(got) != 1 || got[0] != selfContainerID {
		t.Errorf("selfIDsFromMountinfo = %v, want [%s]", got, selfContainerID)
	}
}

// overlay2 layer directories are also 64 hex characters. Matching one would
// yield a confident, wholly wrong id — worse than none — so the "/containers/"
// anchor must exclude them.
//
// The layer hash must sit in a field selfIDsFromMountinfo actually reads (root
// or source). A `lowerdir=` super-option is never scanned, so putting it there
// would make this pass no matter what the regex did.
func TestSelfIDsFromMountinfoIgnoresOverlayLayers(t *testing.T) {
	const layer = "2c6e5b7f8a9d0e1f2a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8091"
	in := "2038 1905 0:186 / / rw - overlay overlay rw,lowerdir=/var/lib/docker/overlay2/" + layer + "/fs\n" +
		"2039 2038 259:2 /var/lib/docker/overlay2/" + layer + "/diff /layer rw - ext4 /dev/nvme0n1p2 rw\n" +
		"2040 2038 259:2 / /host-layers rw - ext4 /var/lib/docker/overlay2/" + layer + "/diff rw\n"
	if got := selfIDsFromMountinfo(parseMountinfo(in)); len(got) != 0 {
		t.Errorf("selfIDsFromMountinfo = %v, want none", got)
	}
}

func TestSelfIDsFromCgroup(t *testing.T) {
	for name, in := range map[string]string{
		"v1 docker":       "12:memory:/docker/" + selfContainerID + "\n11:cpu:/docker/" + selfContainerID + "\n",
		"v2 systemd":      "0::/system.slice/docker-" + selfContainerID + ".scope\n",
		"cri-containerd":  "0::/kubepods.slice/cri-containerd-" + selfContainerID + ".scope\n",
		"crio":            "0::/kubepods.slice/crio-" + selfContainerID + ".scope\n",
		"bare id segment": "11:devices:/kubepods/besteffort/podabc/" + selfContainerID + "\n",
	} {
		got := selfIDsFromCgroup(in)
		if len(got) != 1 || got[0] != selfContainerID {
			t.Errorf("%s: selfIDsFromCgroup = %v, want [%s]", name, got, selfContainerID)
		}
	}
}

// A plain cgroup v2 container sees only "0::/" — no id to mine, which is the
// case that makes CORNUS_HOST_PATH_MAP the only answer.
func TestSelfIDsFromCgroupBare(t *testing.T) {
	if got := selfIDsFromCgroup("0::/\n"); len(got) != 0 {
		t.Errorf("selfIDsFromCgroup = %v, want none", got)
	}
}

func TestSelfIDFromHostname(t *testing.T) {
	if got := selfIDFromHostname("1f0a2b3c4d5e"); got != "1f0a2b3c4d5e" {
		t.Errorf("selfIDFromHostname(short id) = %q", got)
	}
	// An operator --hostname or a Kubernetes pod name is not a candidate at all.
	for _, in := range []string{"cornus-server", "web-7d9f8b6c5d-abcde", "", "1F0A2B3C4D5E"} {
		if got := selfIDFromHostname(in); got != "" {
			t.Errorf("selfIDFromHostname(%q) = %q, want \"\"", in, got)
		}
	}
}

func TestSelfIDCandidatesOrderAndDedupe(t *testing.T) {
	cgroup := "0::/system.slice/docker-" + selfContainerID + ".scope\n"
	got := selfIDCandidates(parseMountinfo(sampleMountinfo), cgroup, "1f0a2b3c4d5e")
	// mountinfo and cgroup agree, so the 64-hex id appears once, ahead of the
	// weaker HOSTNAME-derived short id.
	want := []string{selfContainerID, "1f0a2b3c4d5e"}
	if len(got) != len(want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("candidates[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
