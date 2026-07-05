package server

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPeerKeyFileLifecycle(t *testing.T) {
	dir := t.TempDir()
	first, err := loadPeerKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.PrivatePEM) == 0 || len(first.PublicPEM) == 0 {
		t.Fatal("generated peer keypair is incomplete")
	}
	path := filepath.Join(dir, peerKeyFile)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("peer key mode = %04o, want 0600", info.Mode().Perm())
	}
	second, err := loadPeerKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.PrivatePEM, second.PrivatePEM) || !bytes.Equal(first.PublicPEM, second.PublicPEM) {
		t.Fatal("peer keypair was not stable across reload")
	}
}

func TestPeerKeyRejectsLoosePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not available")
	}
	dir := t.TempDir()
	generated, err := loadPeerKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, peerKeyFile)
	if err := os.WriteFile(path, generated.PrivatePEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPeerKey(dir); err == nil || !strings.Contains(err.Error(), "insecure permissions") {
		t.Fatalf("loose peer key error = %v", err)
	}
}

func TestPeerKeyAbsentWhenNotNeeded(t *testing.T) {
	for _, tc := range []struct {
		name        string
		authEnabled bool
		distributed bool
	}{
		{name: "auth off", authEnabled: false, distributed: true},
		{name: "single replica", authEnabled: true, distributed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			keypair, err := loadPeerKeyIfNeeded(dir, tc.authEnabled, tc.distributed)
			if err != nil {
				t.Fatal(err)
			}
			if keypair != nil {
				t.Fatal("created an unnecessary peer keypair")
			}
			if _, err := os.Stat(filepath.Join(dir, peerKeyFile)); !os.IsNotExist(err) {
				t.Fatalf("peer key exists unexpectedly: %v", err)
			}
		})
	}
}
