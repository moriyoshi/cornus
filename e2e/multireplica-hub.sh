#!/usr/bin/env bash
# Real end-to-end validation of the multi-replica hub against a REAL Redis and TWO
# real cornus server processes (not miniredis, not in-process) -- the thing the
# unit/integration test (pkg/server/hub_multireplica_test.go, miniredis) can
# only approximate.
#
# It stands up: a real Redis (a docker container); replica A and replica B, two
# `cornus serve` processes sharing that Redis (distinct CORNUS_REPLICA_ID and
# CORNUS_HUB_FORWARD_URL); a TCP echo (the delivery target); a spoke that
# REGISTERS "greeter" via A (delivery, so A owns the connection); and a spoke that
# REACHES "greeter" via B. Dialing B's reach listener must forward B -> A -> the
# hosting spoke -> the echo and round-trip -- proving cross-replica delivery
# forwarding through the shared Redis registry.
#
# Requires: docker (for Redis) and go (to build cornus + the echo). Skips cleanly
# if docker is unavailable. Run from the repo root:  bash e2e/multireplica-hub.sh
set -euo pipefail

if ! docker version >/dev/null 2>&1; then
    echo "multireplica-hub: SKIP (docker unavailable; needed for a real Redis)"
    exit 0
fi

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
REDIS_NAME="cornus-mr-redis-$$"
REDIS_PORT=6399
PIDS=()
cleanup() {
    for p in "${PIDS[@]:-}"; do kill "$p" 2>/dev/null || true; done
    docker rm -f "$REDIS_NAME" >/dev/null 2>&1 || true
    rm -rf "$WORK"
}
trap cleanup EXIT

echo "== build cornus + echo =="
go build -o "$WORK/cornus" "$ROOT/cmd/cornus"
cat > "$WORK/echo.go" <<'EOF'
package main
import ("io";"net";"os")
func main(){ ln,e:=net.Listen("tcp",os.Args[1]); if e!=nil{panic(e)}; for{ c,e:=ln.Accept(); if e!=nil{return}; go func(c net.Conn){ io.Copy(c,c); c.Close() }(c) } }
EOF
go build -o "$WORK/echo" "$WORK/echo.go"

echo "== start real Redis =="
docker rm -f "$REDIS_NAME" >/dev/null 2>&1 || true
docker run -d --name "$REDIS_NAME" -p "$REDIS_PORT:6379" redis:7-alpine >/dev/null
for _ in $(seq 1 15); do docker exec "$REDIS_NAME" redis-cli ping 2>/dev/null | grep -q PONG && break; sleep 1; done

export CORNUS_HUB_REDIS="redis://127.0.0.1:$REDIS_PORT"

"$WORK/echo" 127.0.0.1:9000 & PIDS+=($!)
CORNUS_REPLICA_ID=repA CORNUS_HUB_FORWARD_URL=ws://127.0.0.1:5001 \
    "$WORK/cornus" serve --addr 127.0.0.1:5001 --storage mem:// >"$WORK/A.log" 2>&1 & PIDS+=($!)
CORNUS_REPLICA_ID=repB CORNUS_HUB_FORWARD_URL=ws://127.0.0.1:5002 \
    "$WORK/cornus" serve --addr 127.0.0.1:5002 --storage mem:// >"$WORK/B.log" 2>&1 & PIDS+=($!)
sleep 2

echo "== spokes: register greeter via A (delivery), reach it via B =="
"$WORK/cornus" hub --server ws://127.0.0.1:5001 --register greeter=127.0.0.1:9000 >"$WORK/s1.log" 2>&1 & PIDS+=($!)
"$WORK/cornus" hub --server ws://127.0.0.1:5002 --reach greeter=127.0.0.1:9500 >"$WORK/s2.log" 2>&1 & PIDS+=($!)

echo "== cross-replica reach (dial B:9500 -> B -> forward to A -> echo) =="
got=""
for _ in $(seq 1 20); do
    got="$(printf 'PING-MULTIREPLICA\n' | nc -w2 127.0.0.1 9500 2>/dev/null || true)"
    [ -n "$got" ] && break
    sleep 1
done

if [ "$got" = "PING-MULTIREPLICA" ]; then
    echo "PASS: cross-replica delivery forwarding through real Redis works (B forwarded to A)"
    exit 0
fi
echo "FAIL: reach did not round-trip (got: [$got])"
echo "--- A.log ---"; tail -15 "$WORK/A.log" || true
echo "--- B.log ---"; tail -15 "$WORK/B.log" || true
exit 1
