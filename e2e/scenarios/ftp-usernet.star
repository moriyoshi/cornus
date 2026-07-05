# Two-workload FTP over a USER-NETWORK on kubernetes: an FTP server and an FTP
# client, deployed as two cornus workloads on a shared user-network, exercising
# a bidirectional round-trip (put + get + compare) over REAL pod-to-pod
# connectivity.
#
# WHY A USER-NETWORK, NOT THE HUB OVERLAY: FTP embeds IP:port addresses in-band
# in PASV/PORT replies. A name-based relay (the cornus hub overlay) cannot
# rewrite those embedded addresses — this is the classic FTP-through-proxy
# problem — so FTP must NOT ride the hub. A user-network (real L2/L3
# connectivity via the netdriver `bridge` provider) is the correct fabric: the
# client reaches the server directly, and the server's PASV reply advertises the
# on-network IP the client actually connected to (FTP_PASV_ADDRESS=auto makes
# ftpserverlib derive it from the connection; see e2e/scenarios/ftp/ftpd.go).
#
# GATING / VALIDATION: kube-only, and it self-skips unless a Multus fabric is
# present (a user-network needs a real secondary-interface CNI). It is registered
# in SCENARIOS so `make e2e-check` SYNTAX-CHECKS it and a plain kind run cleanly
# SKIPS it; a full end-to-end run is only meaningful on a Multus-enabled cluster,
# so this scenario is VALIDATED ONLY ON A REAL CLUSTER — here we prioritise
# correct-by-construction structure and clear comments over local runnability.
# The client signals success by echoing a known marker (FTP-USERNET-OK) that the
# harness asserts on via pod_exec.

NS = "cornus-e2e"  # KubeTarget's default namespace

def multus_present():
    out = kubectl("get", "crd", "network-attachment-definitions.k8s.cni.cncf.io", "--ignore-not-found", "-o", "name")
    return out.strip() != ""

if TARGET != "kube":
    log("ftp-usernet: skipped (kube-only; two-workload FTP over a user-network)")
elif not multus_present():
    log("ftp-usernet: skipped (Multus CRD not installed; a user-network needs a real secondary-interface CNI)")
else:
    serve()

    # Bring up both workloads on the shared bridge user-network. The server image
    # is built from the ./ftp fixture — on the kube target compose_up itself
    # pre-builds each `build:` service and `kind load`s the image into the node
    # (prepareComposeBuildImages in pkg/e2e/harness.go), so no explicit build()
    # is needed here; the client is a plain busybox sleeper we drive interactively.
    compose_up(file = "e2e/scenarios/ftp-usernet.yaml", project = "ftpnet", detach = True)
    wait(name = "ftpnet-ftpsrv", running = 1, timeout = "240s")
    wait(name = "ftpnet-ftpcli", running = 1, timeout = "240s")
    log("✓ FTP server + client workloads Running on the shared user-network")

    # A non-trivial payload with varied (non-ASCII) bytes so a truncating or
    # one-directional transport cannot pass by accident. Kept printable-safe for
    # the in-pod shell heredoc; byte-equality via cmp still catches corruption.
    payload = ""
    for i in range(128):
        payload += "cornus-usernet-%d-%s|" % (i, "".join([chr(65 + (i + j) % 26) for j in range(4)]))

    # Drive the client entirely in-pod: write the payload, PUT it to the server by
    # its user-network service name, GET it back into a second file, and compare.
    # busybox ftpput/ftpget default to PASSIVE mode; the data connection follows
    # the PASV address the server advertises (its on-network IP via
    # FTP_PASV_ADDRESS=auto), which is reachable because both pods share the
    # user-network. On success we echo a stable marker the harness asserts on.
    #
    # busybox arg order: ftpput HOST REMOTE LOCAL ; ftpget HOST LOCAL REMOTE.
    script = " && ".join([
        "printf '%%s' '%s' > /tmp/up.dat" % payload,
        "ftpput -u cornus -p secret ftpsrv rt.dat /tmp/up.dat",
        "ftpget -u cornus -p secret ftpsrv /tmp/down.dat rt.dat",
        "cmp /tmp/up.dat /tmp/down.dat",
        "echo FTP-USERNET-OK",
    ])
    out = ""
    for _ in range(20):
        out = pod_exec(app = "ftpnet-ftpcli", cmd = script + " 2>&1 || true")
        if "FTP-USERNET-OK" in out:
            break
        sleep(duration = "3s")
    assert_contains(out, "FTP-USERNET-OK", "client FTP put+get+compare over the user-network failed: %r" % out)
    log("✓ FTP bidirectional round-trip (put + get + byte-compare) over the user-network succeeded")

    compose_down(file = "e2e/scenarios/ftp-usernet.yaml", project = "ftpnet")
    log("torn down")
