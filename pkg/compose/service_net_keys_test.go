package compose

import (
	"reflect"
	"strings"
	"testing"
)

// TestServiceNetKeysTranslate asserts the security & networking batch
// (cap_add, cap_drop, security_opt, group_add, sysctls, extra_hosts, dns,
// dns_search, dns_opt) parses and populates the matching api.DeploySpec fields.
// It exercises the LIST form of sysctls-adjacent keys, the MAP form of sysctls,
// the LIST form of extra_hosts, and the LIST form of dns.
func TestServiceNetKeysTranslate(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: nginx
    cap_add:
      - NET_ADMIN
      - SYS_TIME
    cap_drop:
      - MKNOD
    security_opt:
      - no-new-privileges:true
      - label=type:svirt_apache
    group_add:
      - "1001"
      - staff
    sysctls:
      net.core.somaxconn: "1024"
      net.ipv4.tcp_syncookies: "0"
    extra_hosts:
      - "somehost:162.242.195.82"
      - "otherhost:50.31.209.229"
    dns:
      - 8.8.8.8
      - 1.1.1.1
    dns_search:
      - example.com
      - example.org
    dns_opt:
      - "timeout:2"
      - use-vc
`)
	proj, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := proj.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	spec := plans["web"].Spec

	if !reflect.DeepEqual(spec.CapAdd, []string{"NET_ADMIN", "SYS_TIME"}) {
		t.Errorf("CapAdd = %v", spec.CapAdd)
	}
	if !reflect.DeepEqual(spec.CapDrop, []string{"MKNOD"}) {
		t.Errorf("CapDrop = %v", spec.CapDrop)
	}
	if !reflect.DeepEqual(spec.SecurityOpt, []string{"no-new-privileges:true", "label=type:svirt_apache"}) {
		t.Errorf("SecurityOpt = %v", spec.SecurityOpt)
	}
	if !reflect.DeepEqual(spec.GroupAdd, []string{"1001", "staff"}) {
		t.Errorf("GroupAdd = %v", spec.GroupAdd)
	}
	if spec.Sysctls["net.core.somaxconn"] != "1024" || spec.Sysctls["net.ipv4.tcp_syncookies"] != "0" {
		t.Errorf("Sysctls = %v", spec.Sysctls)
	}
	if !reflect.DeepEqual(spec.ExtraHosts, []string{"somehost:162.242.195.82", "otherhost:50.31.209.229"}) {
		t.Errorf("ExtraHosts = %v", spec.ExtraHosts)
	}
	if !reflect.DeepEqual(spec.DNSServers, []string{"8.8.8.8", "1.1.1.1"}) {
		t.Errorf("DNSServers = %v", spec.DNSServers)
	}
	if !reflect.DeepEqual(spec.DNSSearch, []string{"example.com", "example.org"}) {
		t.Errorf("DNSSearch = %v", spec.DNSSearch)
	}
	if !reflect.DeepEqual(spec.DNSOptions, []string{"timeout:2", "use-vc"}) {
		t.Errorf("DNSOptions = %v", spec.DNSOptions)
	}
}

// TestServiceNetKeysAltForms asserts the alternate compose scalar/map forms
// decode identically: dns/dns_search as a single scalar, sysctls as a KEY=VALUE
// list, and extra_hosts as a host->ip map (normalised to "host:ip", sorted).
func TestServiceNetKeysAltForms(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: nginx
    dns: 8.8.8.8
    dns_search: example.com
    sysctls:
      - net.core.somaxconn=1024
    extra_hosts:
      zeta: 10.0.0.2
      alpha: 10.0.0.1
`)
	proj, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := proj.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	spec := plans["web"].Spec

	if !reflect.DeepEqual(spec.DNSServers, []string{"8.8.8.8"}) {
		t.Errorf("DNSServers (scalar) = %v", spec.DNSServers)
	}
	if !reflect.DeepEqual(spec.DNSSearch, []string{"example.com"}) {
		t.Errorf("DNSSearch (scalar) = %v", spec.DNSSearch)
	}
	if spec.Sysctls["net.core.somaxconn"] != "1024" {
		t.Errorf("Sysctls (list form) = %v", spec.Sysctls)
	}
	// Map form normalises to "host:ip" and sorts by host for determinism.
	if !reflect.DeepEqual(spec.ExtraHosts, []string{"alpha:10.0.0.1", "zeta:10.0.0.2"}) {
		t.Errorf("ExtraHosts (map form) = %v", spec.ExtraHosts)
	}
}

// TestServiceNetKeysNotWarned asserts none of the nine batch keys produce an
// unsupported-field warning once plumbed through.
func TestServiceNetKeysNotWarned(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: nginx
    cap_add:
      - NET_ADMIN
    cap_drop:
      - MKNOD
    security_opt:
      - no-new-privileges:true
    group_add:
      - "1001"
    sysctls:
      net.core.somaxconn: "1024"
    extra_hosts:
      - "somehost:10.0.0.1"
    dns:
      - 8.8.8.8
    dns_search:
      - example.com
    dns_opt:
      - use-vc
`)
	lines := captureWarnings(t, func() {
		if _, err := Load(file); err != nil {
			t.Fatalf("Load: %v", err)
		}
	})
	for _, key := range []string{"cap_add", "cap_drop", "security_opt", "group_add", "sysctls", "extra_hosts", "dns", "dns_search", "dns_opt"} {
		for _, ln := range lines {
			if strings.Contains(ln, `field "`+key+`"`) {
				t.Errorf("key %q unexpectedly warned: %s", key, ln)
			}
		}
	}
}

// TestServiceNetKeysMerge asserts the list keys are additive across files
// (append-dedup) and sysctls merges key-by-key with the override winning.
func TestServiceNetKeysMerge(t *testing.T) {
	base := writeFiles(t, map[string]string{"compose.yaml": `
services:
  web:
    image: nginx
    cap_add:
      - NET_ADMIN
    dns:
      - 8.8.8.8
    sysctls:
      net.core.somaxconn: "1024"
      keep: "1"
`})
	override := writeFiles(t, map[string]string{"compose.yaml": `
services:
  web:
    image: nginx
    cap_add:
      - SYS_TIME
      - NET_ADMIN
    dns:
      - 1.1.1.1
    sysctls:
      net.core.somaxconn: "2048"
`})
	proj, err := Load(base, override)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	svc := proj.Services()["web"]
	// append-dedup: base first, override's new entry appended, dup dropped.
	if !reflect.DeepEqual([]string(svc.CapAdd), []string{"NET_ADMIN", "SYS_TIME"}) {
		t.Errorf("CapAdd merge = %v", svc.CapAdd)
	}
	if !reflect.DeepEqual([]string(svc.DNS), []string{"8.8.8.8", "1.1.1.1"}) {
		t.Errorf("DNS merge = %v", svc.DNS)
	}
	// sysctls merge key-by-key, override wins on the shared key.
	if svc.Sysctls["net.core.somaxconn"] != "2048" || svc.Sysctls["keep"] != "1" {
		t.Errorf("Sysctls merge = %v", svc.Sysctls)
	}
}
