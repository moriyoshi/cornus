# Compose provider services (compose-spec `provider:`), driven end to end through
# the real `cornus compose` client.
#
# A provider service delegates its lifecycle to an external plugin binary that
# runs CLIENT-SIDE (on the machine invoking `cornus compose`, never on the
# server), so the core lifecycle — plugin discovery, up/stop/down invocation with
# the right option flags, and `ps` rendering — is backend-agnostic and asserted on
# every target via a stub plugin that records each invocation. On kube we
# additionally deploy a dependent workload and read its environment back with
# pod_exec to prove the plugin's `setenv` output is injected into dependents.

serve()

state = temp_dir()  # the stub plugin records each invocation here (client-side)
work = temp_dir()

# A stub provider plugin (POSIX sh): it records the argv and service name of each
# lifecycle call, and on `up` emits the compose provider stdout protocol — an
# `info` line plus two `setenv` variables. `cornus` locates it by the absolute
# path given as the provider `type:` below (exercising the binary lookup without
# polluting PATH).
plugin = work + "/awesomecloud"
plugin_src = '''#!/bin/sh
cmd=""
for a in "$@"; do
  case "$a" in
    up) cmd=up ;;
    down) cmd=down ;;
    stop) cmd=stop ;;
  esac
  last="$a"
done
echo "$*" > "__STATE__/argv.$cmd"
echo "$last" > "__STATE__/service.$cmd"
if [ "$cmd" = up ]; then
  echo '{"type":"info","message":"provisioning"}'
  echo '{"type":"setenv","message":"URL=mysql://db.example:3306"}'
  echo '{"type":"setenv","message":"TOKEN=s3cr3t"}'
fi
exit 0
'''
write_file(path = plugin, content = plugin_src.replace("__STATE__", state))
sh(cmd = "chmod +x '" + plugin + "'")

# The provider service names the stub by absolute path and passes it two options,
# which must reach the plugin as sorted `--key=value` flags. On kube we add a
# dependent workload so we can observe env injection.
compose_src = '''name: prov
services:
  database:
    provider:
      type: __PLUGIN__
      options:
        engine: mysql
        version: "8"
'''
if TARGET == "kube":
    compose_src += '''  app:
    image: busybox:1.36
    command: ["sh", "-c", "echo ready; sleep 3600"]
    depends_on:
      - database
'''
compose_file = work + "/compose.yaml"
write_file(path = compose_file, content = compose_src.replace("__PLUGIN__", plugin))

# `up` (detached: a provider-only project hands nothing to the background agent
# and returns at once) invokes the plugin's `up` with the sorted option flags and
# the service name as the final positional argument.
compose_up(file = compose_file, project = "prov", detach = True)
up_argv = read_file(path = state + "/argv.up")
log("provider up argv: " + up_argv.strip())
assert_contains(up_argv, "up")
assert_contains(up_argv, "--engine=mysql")
assert_contains(up_argv, "--version=8")
assert_eq(read_file(path = state + "/service.up").strip(), "database", "provider up got the wrong service name")

# `ps` renders the provider service as `provider:<type>`, not a deployed workload.
ps = compose_ps(file = compose_file, project = "prov")
log(ps)
assert_contains(ps, "database")
assert_contains(ps, "provider")

# On kube, the plugin's `setenv` output is injected into the dependent's
# environment, prefixed with the upper-cased provider service name.
if TARGET == "kube":
    wait(name = "prov-app", running = 1, timeout = "180s")
    url = pod_exec(app = "prov-app", cmd = "printenv DATABASE_URL")
    assert_contains(url, "mysql://db.example:3306", "provider setenv URL not injected into dependent")
    token = pod_exec(app = "prov-app", cmd = "printenv DATABASE_TOKEN")
    assert_contains(token, "s3cr3t", "provider setenv TOKEN not injected into dependent")
    log("✓ provider setenv injected into dependent as DATABASE_URL / DATABASE_TOKEN")

# `stop` routes to the plugin's own `stop` verb (compose-spec provider stop),
# distinct from the server-side lifecycle action a normal service would take.
compose_stop(file = compose_file, project = "prov")
assert_eq(read_file(path = state + "/service.stop", default = "").strip(), "database", "compose stop did not invoke the provider plugin's stop")
log("✓ compose stop invoked the provider plugin's stop")

# `down` tears the provider down via the plugin's `down`.
compose_down(file = compose_file, project = "prov")
assert_contains(read_file(path = state + "/argv.down"), "down")
assert_eq(read_file(path = state + "/service.down", default = "").strip(), "database", "compose down did not invoke the provider plugin's down")
log("✓ compose down invoked the provider plugin's down")
