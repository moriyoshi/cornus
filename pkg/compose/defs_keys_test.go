package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile writes content to path, failing the test on error.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestVolumeDefFields checks that a top-level volume's driver / driver_opts /
// labels are parsed and carried onto the named volume's api.VolumeSpec.
func TestVolumeDefFields(t *testing.T) {
	file := writeCompose(t, `
name: proj
services:
  app:
    image: localhost:5000/app:v1
    volumes:
      - "data:/var/lib/data"
volumes:
  data:
    driver: local
    driver_opts:
      type: nfs
      o: addr=10.0.0.1,rw
      device: ":/exports/data"
    labels:
      env: prod
      tier: "1"
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	def := project.Volumes()["data"]
	if def.Driver != "local" {
		t.Errorf("volume driver = %q, want local", def.Driver)
	}
	if def.DriverOpts["type"] != "nfs" || def.DriverOpts["o"] != "addr=10.0.0.1,rw" || def.DriverOpts["device"] != ":/exports/data" {
		t.Errorf("volume driver_opts = %v", def.DriverOpts)
	}
	if def.Labels["env"] != "prod" || def.Labels["tier"] != "1" {
		t.Errorf("volume labels = %v", def.Labels)
	}

	plans, err := project.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	vols := plans["app"].Spec.Volumes
	if len(vols) != 1 {
		t.Fatalf("volumes = %v", vols)
	}
	vs := vols[0]
	if vs.Name != "proj_data" {
		t.Errorf("volume name = %q, want proj_data", vs.Name)
	}
	if vs.Driver != "local" {
		t.Errorf("VolumeSpec.Driver = %q, want local", vs.Driver)
	}
	if vs.DriverOpts["type"] != "nfs" || vs.DriverOpts["device"] != ":/exports/data" {
		t.Errorf("VolumeSpec.DriverOpts = %v", vs.DriverOpts)
	}
	if vs.Labels["env"] != "prod" {
		t.Errorf("VolumeSpec.Labels = %v", vs.Labels)
	}
}

// TestNetworkDefFields checks that a top-level network's ipam config, the
// attachable/internal/enable_ipv6 toggles, and labels are parsed and carried
// onto every member's api.NetworkAttachment.
func TestNetworkDefFields(t *testing.T) {
	file := writeCompose(t, `
name: proj
services:
  app:
    image: localhost:5000/app:v1
    networks:
      - backend
networks:
  backend:
    driver: bridge
    internal: true
    attachable: true
    enable_ipv6: true
    labels:
      team: infra
    ipam:
      config:
        - subnet: 172.28.0.0/16
          gateway: 172.28.0.1
          ip_range: 172.28.5.0/24
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	def := project.Networks()["backend"]
	if !def.Internal || !def.Attachable || !def.EnableIPv6 {
		t.Errorf("network toggles = internal:%v attachable:%v enable_ipv6:%v", def.Internal, def.Attachable, def.EnableIPv6)
	}
	if def.Labels["team"] != "infra" {
		t.Errorf("network labels = %v", def.Labels)
	}
	if def.IPAM == nil || len(def.IPAM.Config) != 1 {
		t.Fatalf("ipam = %+v", def.IPAM)
	}
	c := def.IPAM.Config[0]
	if c.Subnet != "172.28.0.0/16" || c.Gateway != "172.28.0.1" || c.IPRange != "172.28.5.0/24" {
		t.Errorf("ipam config = %+v", c)
	}

	plans, err := project.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	nets := plans["app"].Spec.Networks
	if len(nets) != 1 {
		t.Fatalf("networks = %v", nets)
	}
	na := nets[0]
	if na.Name != "proj_backend" {
		t.Errorf("network name = %q, want proj_backend", na.Name)
	}
	if na.Subnet != "172.28.0.0/16" || na.Gateway != "172.28.0.1" || na.IPRange != "172.28.5.0/24" {
		t.Errorf("attachment ipam = subnet:%q gateway:%q iprange:%q", na.Subnet, na.Gateway, na.IPRange)
	}
	if !na.Internal || !na.Attachable || !na.EnableIPv6 {
		t.Errorf("attachment toggles = internal:%v attachable:%v enable_ipv6:%v", na.Internal, na.Attachable, na.EnableIPv6)
	}
	if na.Labels["team"] != "infra" {
		t.Errorf("attachment labels = %v", na.Labels)
	}
}

// TestServiceNetworkLongFormFields checks the service `networks:` long syntax
// ipv6_address / mac_address / priority are parsed and carried onto the
// api.NetworkAttachment (alongside the existing aliases / ipv4_address).
func TestServiceNetworkLongFormFields(t *testing.T) {
	file := writeCompose(t, `
name: proj
services:
  app:
    image: localhost:5000/app:v1
    networks:
      backend:
        aliases:
          - app.internal
        ipv4_address: 172.28.1.5
        ipv6_address: 2001:db8::5
        mac_address: 02:42:ac:11:00:05
        priority: 100
networks:
  backend: {}
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	sn := project.Services()["app"].Networks
	if len(sn) != 1 {
		t.Fatalf("service networks = %v", sn)
	}
	if sn[0].IPv6Address != "2001:db8::5" || sn[0].MacAddress != "02:42:ac:11:00:05" || sn[0].Priority != 100 {
		t.Errorf("service network long form = %+v", sn[0])
	}

	plans, err := project.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	na := plans["app"].Spec.Networks[0]
	if na.IPv6 != "2001:db8::5" {
		t.Errorf("attachment IPv6 = %q", na.IPv6)
	}
	if na.MAC != "02:42:ac:11:00:05" {
		t.Errorf("attachment MAC = %q", na.MAC)
	}
	if na.Priority != 100 {
		t.Errorf("attachment Priority = %d", na.Priority)
	}
}

// TestDefsKeysMerge checks the multi-file deep-merge rules for the new def
// fields: volume/network driver_opts + labels merge key-by-key, network bool
// toggles override when set, the ipam block override-replaces, and service
// network ipv6/mac/priority override when the override sets them.
func TestDefsKeysMerge(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yaml")
	over := filepath.Join(dir, "over.yaml")
	writeFile(t, base, `
name: proj
services:
  app:
    image: localhost:5000/app:v1
    networks:
      backend:
        ipv6_address: 2001:db8::1
        priority: 5
volumes:
  data:
    driver: local
    driver_opts:
      type: nfs
    labels:
      a: "1"
networks:
  backend:
    internal: false
    labels:
      x: base
    ipam:
      config:
        - subnet: 10.0.0.0/24
`)
	writeFile(t, over, `
services:
  app:
    networks:
      backend:
        mac_address: 02:42:ac:11:00:09
        priority: 50
volumes:
  data:
    driver_opts:
      o: rw
    labels:
      b: "2"
networks:
  backend:
    internal: true
    labels:
      zone: over
    ipam:
      config:
        - subnet: 192.168.0.0/24
          gateway: 192.168.0.1
`)
	project, err := Load(base, over)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	vd := project.Volumes()["data"]
	if vd.Driver != "local" {
		t.Errorf("merged volume driver = %q", vd.Driver)
	}
	if vd.DriverOpts["type"] != "nfs" || vd.DriverOpts["o"] != "rw" {
		t.Errorf("merged volume driver_opts = %v", vd.DriverOpts)
	}
	if vd.Labels["a"] != "1" || vd.Labels["b"] != "2" {
		t.Errorf("merged volume labels = %v", vd.Labels)
	}

	nd := project.Networks()["backend"]
	if !nd.Internal {
		t.Errorf("merged network internal = %v, want true (override sets it)", nd.Internal)
	}
	if nd.Labels["x"] != "base" || nd.Labels["zone"] != "over" {
		t.Errorf("merged network labels = %v", nd.Labels)
	}
	// ipam override-replaces the base block wholesale.
	if nd.IPAM == nil || len(nd.IPAM.Config) != 1 || nd.IPAM.Config[0].Subnet != "192.168.0.0/24" || nd.IPAM.Config[0].Gateway != "192.168.0.1" {
		t.Errorf("merged ipam = %+v", nd.IPAM)
	}

	sn := project.Services()["app"].Networks[0]
	if sn.IPv6Address != "2001:db8::1" {
		t.Errorf("merged service ipv6 = %q, want base kept", sn.IPv6Address)
	}
	if sn.MacAddress != "02:42:ac:11:00:09" {
		t.Errorf("merged service mac = %q, want override", sn.MacAddress)
	}
	if sn.Priority != 50 {
		t.Errorf("merged service priority = %d, want 50 (override)", sn.Priority)
	}
}
