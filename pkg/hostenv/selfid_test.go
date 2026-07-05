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

// TestSelfIDsFromMountinfoPodman covers podman's per-container bind spelling.
//
// This is not cosmetic parity with Docker: podman NEEDS the mountinfo miner,
// where Docker only benefits from it. Docker sets a container's HOSTNAME to its
// own short id, so the hostname miner alone identifies it. Podman generates a
// RANDOM hostname unrelated to the container id — measured: a container with id
// 3cde70d7... had hostname 3958b35611d1 — so mining the hostname yields a
// candidate the daemon rejects with "no such container", and a co-located cornus
// ends up unable to identify itself at all.
//
// The consequence of failing to identify itself is concrete: selfNetworkScope
// refuses to attach an unconfirmed container to a workload's network, so an
// in-container cornus on a rootless podman loses the direct route that topology
// exists to provide.
func TestSelfIDsFromMountinfoPodman(t *testing.T) {
	const id = "3cde70d740867c5e78bdaf734e7e311c08b1dd87aa57adc6c406ca11ef3d17e7"
	entries := []mountEntry{
		// Real line shape from podman 5.8.2, rootless.
		{root: "/tmp/storage-run-1000/containers/overlay-containers/" + id + "/userdata/hosts"},
		{root: "/tmp/storage-run-1000/containers/overlay-containers/" + id + "/userdata/resolv.conf"},
	}
	got := selfIDsFromMountinfo(entries)
	if len(got) != 1 || got[0] != id {
		t.Errorf("selfIDsFromMountinfo = %v, want exactly [%s]", got, id)
	}
}

// TestSelfIDsFromMountinfoIgnoresPodmanLayers guards the anchor. Podman's image
// layers are also 64 hex chars; matching one would hand the daemon a confident
// wrong id, which is worse than none — it would make some unrelated container
// look like this process.
func TestSelfIDsFromMountinfoIgnoresPodmanLayers(t *testing.T) {
	const layer = "c509159b1b7b671094c8b829c463018a64387c2afc513f35ccf7b8f05aaf7d59"
	entries := []mountEntry{
		{root: "/var/lib/containers/storage/overlay/" + layer + "/diff"},
		{source: "/var/lib/containers/storage/overlay/" + layer + "/merged"},
		// A volume path also carries "containers/" but no bare id after it.
		{root: "/var/lib/containers/storage/volumes/myvol/_data"},
	}
	if got := selfIDsFromMountinfo(entries); len(got) != 0 {
		t.Errorf("selfIDsFromMountinfo = %v, want none: layer and volume paths are not container ids", got)
	}
}
