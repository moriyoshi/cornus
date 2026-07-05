# Bare-name DNS resolution on a compose user network, asserted AT THE LOOKUP.
#
# Why this scenario exists, and why the assertion is where it is.
#
# Cornus's compose networking rests on one property: a service reaches its peers
# by their compose service NAME. Every backend provides it differently — Docker's
# embedded DNS, netavark/aardvark-dns on podman, a synced hosts file on
# containerd and bare — and on podman it is not on by default. libpod's network
# create takes the request body verbatim and does NOT default `dns_enabled`,
# where the CLI sets it and Podman's own Docker-compat endpoint forces it on for
# bridge networks. Measured on Podman 5.8.2: a network created without it
# resolves peer names as NXDOMAIN.
#
# The failure that shape produces is the reason this is an E2E scenario and not
# a unit test. The network is created successfully. Its inspect looks right. Every
# structural assertion an API-level test could make passes. The only symptom is
# that an application cannot find its database — which reads as an application
# bug, in someone else's code.
#
# So this asserts the LOOKUP, from inside a container, on a network cornus made.
# A test that checked the create request carried `dns_enabled: true` would be
# checking that cornus sends what cornus decided to send; only a resolution
# proves the network answers.
#
# Deliberately backend-agnostic. The trap is podman's, but the property is
# universal and each backend implements it separately, so the same scenario
# guards all four host backends. kube has its own richer coverage in
# deploy-network.star (headless alias Services + CoreDNS), which this would
# duplicate less precisely.

PROJECT = "dnsres"
FILE = "e2e/scenarios/compose-dns-resolution.yaml"

if TARGET == "local":
    log("compose-dns-resolution: skipped (no runtime backend)")
elif TARGET == "kube":
    log("compose-dns-resolution: skipped (kube's equivalent is deploy-network.star, which asserts the headless-Service DNS path)")
else:
    addr = serve()
    compose_up(file = FILE, project = PROJECT, detach = True)
    wait(name = PROJECT + "-srv", running = 1, timeout = "240s")
    wait(name = PROJECT + "-cli", running = 1, timeout = "240s")

    # Resolve first, separately from connecting. The two fail for different
    # reasons and a combined check would report the wrong one: a missing DNS
    # record and a workload that has not finished starting both look like "could
    # not fetch".
    r = exec_tty(argv = [
        "cornus", "exec", "--server", "http://" + addr, PROJECT + "-cli",
        "sh", "-c", "getent hosts srv || echo NXDOMAIN",
    ])
    assert_true(
        "NXDOMAIN" not in r["output"],
        "compose service 'srv' does not resolve by bare name from a peer on the same user network: %r\n" % r["output"] +
        "on podman this is what an unset dns_enabled looks like — the network is created, inspect looks correct, " +
        "and only name resolution is missing",
    )
    log("✓ bare compose service name resolves on the user network")

    # ...and it resolves to something that actually answers, which is what the
    # deployment is for. Resolution to a stale or wrong address would pass the
    # check above.
    got = exec_tty(argv = [
        "cornus", "exec", "--server", "http://" + addr, PROJECT + "-cli",
        "sh", "-c", "wget -qO- --timeout=10 http://srv 2>&1 || true",
    ])
    assert_contains(
        got["output"], "nginx",
        "resolved 'srv' but could not fetch from it: %r" % got["output"],
    )
    log("✓ the resolved name reaches the peer (nginx answered)")

    compose_down(file = FILE, project = PROJECT)
