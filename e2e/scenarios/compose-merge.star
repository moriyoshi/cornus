# Prove multi-file (`-f base -f override`) Compose merge is a field-level deep
# merge, not whole-service replacement. The base sets environment MERGE_A/MERGE_B;
# the override sets MERGE_B/MERGE_C. After merge the running container must see:
#   MERGE_A=base_a      (kept from base -> proves deep merge)
#   MERGE_B=override_b  (override wins)
#   MERGE_C=c           (added by override)
# If MERGE_A is missing, the override replaced the whole service and deep merge
# regressed — report it, don't weaken. Public image, no build.

if TARGET == "local":
    log("compose-merge: skipped (needs a real backend)")
else:
    base = "e2e/scenarios/compose-merge-base.yaml"
    override = "e2e/scenarios/compose-merge-override.yaml"

    addr = serve()
    host = {"CORNUS_HOST": "http://" + addr}
    srv = "http://" + addr

    cornus("compose", "-f", base, "-f", override, "up", "-d", env = host)
    wait(name = "cme-app", running = 1, timeout = "120s")

    r = exec_tty(argv = ["cornus", "exec", "--server", srv, "cme-app", "sh", "-c", "env"])
    envout = r["output"]
    log(envout)

    assert_contains(envout, "MERGE_A=base_a", "MERGE_A missing: override replaced the whole service (deep merge regressed)")
    assert_contains(envout, "MERGE_B=override_b", "MERGE_B override did not win")
    assert_contains(envout, "MERGE_C=c", "MERGE_C from override not added")
    log("✓ multi-file deep merge: base kept, override won, addition applied")

    cornus("compose", "-f", base, "-f", override, "down", env = host)
    assert_eq(status(name = "cme-app")["total"], 0, "cme-app still present after down")
    log("✓ compose multi-file deep merge verified end to end")
