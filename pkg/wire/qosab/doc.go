// Package qosab is the yamux QoS performance harness: an in-process link
// simulator plus a set of Go benchmarks driven entirely by in-code matrices
// (variants x link profiles x scenarios). It has NO environment-variable
// configuration and no stock/fork build toggle — the QoS behavior of each variant
// (scheduler mode + frame cap, including a stock-like FIFO/uncapped one) is a
// yamux.Config value in the matrix. It depends on the in-repo yamux fork (the
// Config knobs and per-stream SetPriority live there).
//
// The benchmarks do not run in the normal `go test ./...` gate; invoke them:
//
//	go test -run '^$' -bench . ./pkg/wire/qosab/
//
// See qosab_test.go for the matrices and link.go / scenarios.go for the simulator
// and workloads. The netemab sub-module runs the same style of matrix over a REAL
// TCP stack (gVisor netstack) with packet loss.
package qosab
