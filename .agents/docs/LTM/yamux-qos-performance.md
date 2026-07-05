# Yamux QoS And Transport Performance

## Summary

Cornus carries a forked MPL-2.0 yamux under `third_party/yamux` to make control and interactive traffic responsive during bulk mount/build streams. Performance changes were selected through reproducible A/B harnesses, including an emulated-link harness and real TCP/netem experiments, and the batched send path is now the production default.

## Key Facts

- QoS classes and configuration must reach the real backing sessions; wiring only the API layer produces misleading benchmark results.
- The batched-pipelined send path is production default after A/B validation. It improves bulk streaming but is neutral for synchronous SQLite random writes.
- Priority scheduling helps within yamux, but TCP head-of-line blocking under loss is the system-level ceiling and needs a transport below yamux to change.
- `third_party/yamux` is modified MPL-2.0 code; retain its license and account for it in notices.

## Details

The fork adds priority scheduling, frame-size controls, and a batched send path behind `yamux.Config` controls. Work began with in-process QoS A/B runs, decomposed frame-cap versus scheduler effects, then verified the actual propagation path. `pkg/wire` and mount traffic receive the classes rather than merely configuring unused defaults.

`pkg/wire/qosab` models controlled links without environment variables. `pkg/wire/netemab` exercises real TCP over an emulated L3 link, including loss, bandwidth, and MTU sweeps. The measurements establish that MTU is a throughput dial on a clean link and that TCP's in-order delivery bounds latency under loss regardless of yamux scheduler quality. Product E2E then confirmed the fork across docker and Kubernetes, and CI runs the nested-module tests.

The later batched-pipelined rework found and regression-tested send-path issues before shipping. It improves suitable bulk transfers; the SQLite block-cache workload instead exposed cache allocation and synchronous round-trip costs, which were addressed in the block-cache layer rather than by attributing them to yamux.

## Files

- `third_party/yamux/` - modified fork, config, priority, and batched send implementation.
- `pkg/wire/qosab/` and `pkg/wire/netemab/` - A/B and real-TCP measurement harnesses.
- `pkg/wire/` and client/caretaker transport construction - QoS class propagation.
- CI workflow and nested-module test wiring - regression coverage.

## Test Coverage

Run the yamux nested-module tests and the QoS/netem harnesses when altering scheduling or frame behavior, then use the docker and Kubernetes E2E suite as the product-level check. Preserve A/B baselines and include the control configuration when reporting a speedup.

## Pitfalls

- Do not infer production behavior from a harness until configuration reaches the real transport backing.
- A transport optimization cannot remove synchronous application round trips or TCP loss head-of-line blocking.
- Treat architecture proposals as hypotheses: measure before committing a default.
