package sshclient

import (
	"context"
	"strings"
	"testing"
)

func TestOutputPureGoSession(t *testing.T) {
	pub, keyPath := writeKey(t, "")
	server, addr := newFakeSSHServer(t, pub)
	server.sessionOutput = "enrollment-code\n"
	out, err := Output(context.Background(), addr, Options{
		Addr: addr, User: "test", IdentityFiles: []string{keyPath}, Insecure: true,
	}, false, "cornus auth enrollment-code")
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if strings.TrimSpace(string(out)) != "enrollment-code" {
		t.Fatalf("output = %q", out)
	}
}
