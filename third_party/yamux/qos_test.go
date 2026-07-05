// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package yamux

import "testing"

// These tests lock the contract of the cornus fork's per-stream QoS API
// (SetPriority / SetMaxWindow) so the intentionally-kept, currently-unused
// SetMaxWindow hook does not bit-rot across upstream merges. They live in the
// fork module (third_party/yamux); run with `cd third_party/yamux && go test`.

func TestSetPriorityClamp(t *testing.T) {
	s := newStream(&Session{config: DefaultConfig()}, 1, streamInit)
	if s.sendClass != ClassNormal {
		t.Fatalf("default sendClass = %d, want ClassNormal(%d)", s.sendClass, ClassNormal)
	}
	s.SetPriority(ClassBulk)
	if s.sendClass != ClassBulk {
		t.Fatalf("SetPriority(ClassBulk): got %d", s.sendClass)
	}
	s.SetPriority(ClassHigh)
	if s.sendClass != ClassHigh {
		t.Fatalf("SetPriority(ClassHigh): got %d", s.sendClass)
	}
	// ClassUrgent is reserved for control frames -> clamped to ClassHigh.
	s.SetPriority(ClassUrgent)
	if s.sendClass != ClassHigh {
		t.Fatalf("SetPriority(ClassUrgent) = %d, want clamp to ClassHigh(%d)", s.sendClass, ClassHigh)
	}
	// Out-of-range likewise clamps.
	s.SetPriority(200)
	if s.sendClass != ClassHigh {
		t.Fatalf("SetPriority(200) = %d, want clamp to ClassHigh(%d)", s.sendClass, ClassHigh)
	}
}

func TestSetMaxWindowClamp(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxStreamWindowSize = 512 << 10 // a non-default so the field is observable
	s := newStream(&Session{config: cfg}, 1, streamInit)
	if s.maxWindow != cfg.MaxStreamWindowSize {
		t.Fatalf("default maxWindow = %d, want config default %d", s.maxWindow, cfg.MaxStreamWindowSize)
	}
	s.SetMaxWindow(8 << 20)
	if s.maxWindow != 8<<20 {
		t.Fatalf("SetMaxWindow(8MiB): got %d", s.maxWindow)
	}
	// Below the initial 256 KiB window is clamped up to it.
	s.SetMaxWindow(1)
	if s.maxWindow != initialStreamWindow {
		t.Fatalf("SetMaxWindow(1) = %d, want clamp up to initialStreamWindow(%d)", s.maxWindow, initialStreamWindow)
	}
}
