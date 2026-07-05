package daemonize

import (
	"reflect"
	"testing"
)

func TestStripDaemonFlag(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"short flag", []string{"daemon", "docker", "-d"}, []string{"daemon", "docker"}},
		{"long flag", []string{"daemon", "docker", "--daemon", "--socket", "/tmp/s.sock"}, []string{"daemon", "docker", "--socket", "/tmp/s.sock"}},
		{"long flag with value", []string{"daemon", "mounts", "--daemon=true", "-p", "proj"}, []string{"daemon", "mounts", "-p", "proj"}},
		{"no flag", []string{"daemon", "docker"}, []string{"daemon", "docker"}},
		{"empty", []string{}, []string{}},
	}
	for _, c := range cases {
		if got := stripDaemonFlag(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: stripDaemonFlag(%v) = %v; want %v", c.name, c.in, got, c.want)
		}
	}
}
