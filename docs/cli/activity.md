# cornus activity

Read the server's flight records: what it and its caretakers were doing, and what
they did not finish.

## Synopsis

```sh
cornus activity [flags]
```

## Description

When a deployment goes wrong, nothing else tells you what happened. `cornus
deploy`/`status` report what is true *right now*, and only for what the runtime
still remembers. Server logs are ephemeral and go with the container. Traces
leave the box entirely and are off by default.

The flight records are different: the server and its caretakers write them to
disk as they work, under the data directory, so they survive the process, the
container, and the incident.

Work is recorded as a **begin/end pair**, which makes the useful question one of
absence — anything that began and never ended did not finish:

- a **process lifetime** that never ended is a server or caretaker that did not
  shut down cleanly (SIGKILL, OOM, `docker rm -f`, a panic, a host reboot);
- a **9P mount** that never ended is a mountpoint that may still exist with
  nobody owning it. Those are undone automatically the next time a server starts;
- a **service** that never ended was still running when its process died. Taken
  together they are the closest thing to a snapshot of what the process was doing
  at the moment it stopped, and a service that crash-loops appears as one pair per
  restart attempt rather than as one long silence.

Every launch takes its own instance id, so records group into runs:

```
server 02c22ece4a16 (exited cleanly)
  2026-07-26T03:53:19.985820864Z server    begin addr=127.0.0.1:5000
  2026-07-26T03:53:22.950323418Z server    end   [ok]

server 6c8ba5e0d63f (DID NOT EXIT CLEANLY)
  2026-07-26T03:53:23.973104203Z server    begin addr=127.0.0.1:5000
  2026-07-26T03:53:24.101339812Z 9p-mount  begin /var/lib/cornus/mounts/sess-1/m0 deployment=web
```

A run that is still going reads as `running`, not as a failure — the command
knows which instance is serving it.

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--server` | connection profile | Remote cornus server URL. |
| `--local` | off | Read the records straight off disk instead of asking a server. |
| `--since` | — | Only records at or after this time: RFC3339, or a duration back from now (`2h`). |
| `--kind` | — | Only this kind: `server`, `caretaker`, `service`, `9p-mount`, `build`, `deploy`. |
| `--unfinished` | off | Only activities that began and never finished. |
| `--follow`, `-f` | off | Print the records, then keep printing them as they are written. Ends on Ctrl-C. |

The directory `--local` reads comes from the global `--data-dir` / `CORNUS_DATA`.

## Following the record

`--follow` prints the history and then stays open, printing each record as it is
written:

```sh
cornus activity --follow --kind 9p-mount     # watch mounts come and go
cornus activity -f --since 5m                # recent history, then live
```

The history and the live tail come from one read, so nothing written between
"what is there" and "start watching" is lost — which matters, because the records
worth following are written exactly when the machine is busy. Ctrl-C is the
normal way to stop and exits 0.

Following works both ways `cornus activity` reads. Remotely the server streams
[Server-Sent Events](https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events)
from `GET /.cornus/v1/activity?follow=1`: a long-lived, mostly idle connection
needs a keep-alive and a media type intermediaries will not buffer, and SSE
defines both. Each record arrives as one `activity` event whose payload is the
same JSON object the one-shot read returns, so `--output json --follow` is an
NDJSON feed of exactly the records `--output json` gives you. With `--local` it
tails the files directly, no server involved.

Grouping by incarnation is not possible live — a run's verdict is only known once
it has ended — so each line names the process and instance that wrote it:

```
2026-07-26T03:53:24.101339812Z server/6c8ba5e0d63f 9p-mount  begin /var/lib/cornus/mounts/sess-1/m0 deployment=web
2026-07-26T03:53:31.884210553Z caretaker/1f2a0b7c9de4 service   begin mount-relay
```

`--follow` and `--unfinished` cannot be combined. "Unfinished" is resolved over
the whole stream — a `begin` is unfinished only until its `end` arrives — so as a
feed it would print records that the next line makes false, with nothing printed
to retract them. Re-run without `--follow` for the snapshot, or follow and pair
`begin`/`end` yourself.

## Remote and post-mortem reads

By default this behaves like every other command and asks the configured server,
because the operator is almost never on the machine the server ran on.

That still answers post-mortem questions. The records live under the **data
directory**, which is the thing cornus deployments keep persistent — the Helm
chart's volume, a containerized server's host bind, a host install's storage dir
— so a *replacement* server serves its predecessor's flight:

```sh
cornus activity --unfinished        # what did the last run leave behind?
cornus activity --since 2h --kind deploy
```

`--local` covers the one case that cannot: nothing is running and nothing is
coming back.

```sh
# on the host, or inside the image, with no server involved
docker run --rm -v /srv/cornus:/var/lib/cornus \
  ghcr.io/moriyoshi/cornus:latest activity --local
```

## Machine-readable output

`--output json` emits the records themselves, so a script or an agent can read
the flight directly:

```sh
cornus --output json activity --unfinished
```

Each record carries its timestamp, the writing process and instance, the kind and
phase, the target, and — on an end record — the outcome. A `recovered` status
means a later run closed the activity: the incident stays visible rather than
being rewritten as a clean completion.

Agent clients reach the same records through [`cornus web`](/cli/web)'s MCP
endpoint, which exposes an `activity_read` tool (the same `since`/`kind`/
`unfinished` filters) and a `cornus://activity/unfinished` resource. The resource
form matters: it is context rather than an action, so a client can attach the
current unfinished set the way it attaches a file, and an agent asked about a
misbehaving deployment starts out already knowing the last server died
mid-flight. Both carry `liveInstance` alongside the records — without it, the
serving process's own open lifetime reads as a crash. Following is CLI-only; a
recorder is read after the fact.

## Retention

The log is size-capped with one retained previous generation, so it is bounded
like any recorder. `CORNUS_ACTIVITY_MAX_BYTES` sets the cap (default 8 MiB).

**See also:** [cornus serve](/cli/serve), [cornus storage](/cli/storage),
[Running the server in a container](/guides/server-in-a-container).
