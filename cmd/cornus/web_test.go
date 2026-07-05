package main

import "testing"

func TestRequireLoopback(t *testing.T) {
	for addr, ok := range map[string]bool{
		"127.0.0.1:0":    true,
		"localhost:8080": true,
		"[::1]:9000":     true,
		":8080":          false,
		"0.0.0.0:8080":   false,
		"192.168.1.5:80": false,
		"example.com:80": false,
	} {
		err := requireLoopback(addr)
		if ok && err != nil {
			t.Errorf("%s: unexpected error %v", addr, err)
		}
		if !ok && err == nil {
			t.Errorf("%s: expected rejection", addr)
		}
	}
}
