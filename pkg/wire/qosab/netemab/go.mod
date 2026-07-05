// netemab is a NESTED module (like third_party/yamux) so ooni/netem's heavy
// gVisor userspace-netstack dependency never enters cornus's main go.mod/go.sum
// and the normal `go test ./...` gate stays fast. It runs the yamux QoS matrix
// over a REAL TCP stack on a netem-emulated L3 link (delay + packet loss +
// bandwidth + MTU), which the lightweight reliable-byte-stream link in ../link.go
// fundamentally cannot model.
//
// It is entirely in-code (no env vars): QoS variants are yamux.Config values. It
// always uses the in-repo yamux fork (the replace below). Run explicitly:
//
//	cd pkg/wire/qosab/netemab && go test -run TestNetem -v
module cornus/pkg/wire/qosab/netemab

go 1.26

require (
	github.com/hashicorp/yamux v0.1.2
	github.com/ooni/netem v0.0.0-20260715150927-e0a456040e27
)

require (
	github.com/google/btree v1.1.2 // indirect
	github.com/google/gopacket v1.1.19 // indirect
	github.com/miekg/dns v1.1.57 // indirect
	golang.org/x/crypto v0.28.0 // indirect
	golang.org/x/mod v0.21.0 // indirect
	golang.org/x/net v0.30.0 // indirect
	golang.org/x/sync v0.8.0 // indirect
	golang.org/x/sys v0.26.0 // indirect
	golang.org/x/time v0.7.0 // indirect
	golang.org/x/tools v0.26.0 // indirect
	gvisor.dev/gvisor v0.0.0-20250317184159-a24f13b091dc // indirect
)

replace github.com/hashicorp/yamux => ../../../../third_party/yamux
