# Wiring microservices together over the hub overlay

## The scenario

Several services are deployed independently — different specs, different rollout
schedules, maybe different nodes, clusters, or even a developer laptop behind
NAT. Each one needs to reach the others by a stable name without hard-coding IPs
or standing up a service mesh. The Cornus server doubles as a **star hub**: each
workload joins as a spoke, registers the services it hosts, and reaches other
spokes' services by name — the hub relays the bytes.

## What you'll use

- The workload-to-workload hub overlay and its relay model — see [the workload hub](/topics/hub).
- The `hub:` block in the deploy spec for in-cluster workloads — see [Deploy spec](/reference/deploy-spec).
- `cornus hub` to join the overlay from anywhere, including a laptop — see [`cornus hub`](/cli/hub) and [Networking recipes](/guides/networking).

## Walkthrough

1. **Deploy the database and export it on the hub.** The `hub:` block joins the
   workload to the overlay. `export` names the services this workload hosts. If
   the hub cannot dial the pod directly, mark the export `deliver: true` so the
   hub relays to the pod, which dials the port on localhost.

   ```yaml
   # db.yaml
   name: db
   image: cornus.example:5000/postgres:16
   hub:
     identity: db
     export:
       - { name: db, port: 5432, deliver: true }
   ```

   ```sh
   cornus deploy -f db.yaml --server http://cornus.example:5000 --detach
   ```

2. **Deploy the API and import the database by name.** `import` lists the
   services this workload reaches. For each import the backend allocates a
   synthetic loopback IP, wires a DNS record, and binds a caretaker listener — so
   a plain connection to `db:5432` from inside the API container funnels into the
   hub with no application changes.

   ```yaml
   # api.yaml
   name: api
   image: cornus.example:5000/api:v1
   env:
     DATABASE_URL: postgres://db:5432/shop
   hub:
     identity: api
     export:
       - { name: api, port: 8080 }
     import:
       - { name: db, ports: [5432] }
   ```

   ```sh
   cornus deploy -f api.yaml --server http://cornus.example:5000 --detach
   ```

   The API reaches the database as `db:5432` and offers itself as `api` for
   anything else that imports it — neither side hard-codes an address.

3. **Reach an overlay service from a laptop.** A developer behind NAT joins the
   same overlay as a spoke with `cornus hub`, binding a local loopback port that
   forwards into the overlay's `db` service. The server is resolved from
   `--server` or the selected connection profile.

   ```sh
   cornus hub --identity laptop --reach db=127.0.0.1:5432
   # now: psql 'host=127.0.0.1 port=5432 dbname=shop ...'
   ```

   The same command can offer a locally running service to the overlay with
   `--register name=host:port`, so a service under development on the laptop is
   reachable by name from the cluster while you iterate.

## How it works

Each participant is a **spoke**; the server is the hub. A spoke registers each
service it hosts in one of two modes:

- **dial-direct** — the service is registered with an address the hub can reach,
  and the hub dials it itself.
- **delivery (relay)** — the service is registered with no reachable address
  (`deliver: true` on an export, or every `cornus hub --register`). To reach it
  the hub opens an ingress stream back to the hosting spoke, which dials its own
  local target and splices. This is what makes a NAT'd laptop or a
  cross-cluster pod reachable — the hub never needs a route to it.

To reach a peer, the source spoke opens a data stream naming the service; the hub
looks it up and either dials it or delivers via the owning spoke, then copies the
bytes. Both TCP and UDP work (a `/udp`-style `protocol: udp` selects UDP). For
in-cluster workloads the whole thing is declared in the `hub:` block; from a
laptop or any host outside the cluster, `cornus hub --register` / `--reach` joins
the same overlay from the CLI. The full field set — `export`, `import`,
`importDynamic`, `identity` — is in the [deploy spec reference](/reference/deploy-spec).

Access is governed by two optional policy matrices, each enforced only when
configured: a **reach** matrix (caller identity to allowed callee services,
`CORNUS_HUB_POLICY`) and a **register** matrix (identity to hostable service
names, `CORNUS_HUB_REGISTER_POLICY`). A spoke declares its `identity`, but under
mTLS the identity is taken from the verified client certificate's CommonName, so
policy keys on a credential the spoke cannot forge. See
[the workload hub](/topics/hub) for the identity and policy model.

## Variations

- **Dynamic discovery.** Instead of a static `import` list, set `importDynamic`
  with a shared port set; the caretaker subscribes to hub catalog pushes and
  binds a listener at every cataloged service as services appear and vanish.
- **UDP services.** Add `protocol: udp` on an export / import for byte-copied
  UDP flows.
- **Cross-backend.** Because the hub relays bytes, spokes on different backends
  or clusters — as long as they connect to the same hub — reach each other the
  same way.

**See also:** [Networking recipes](/guides/networking) · [The workload hub](/topics/hub) · [Deploy spec](/reference/deploy-spec) · [`cornus hub`](/cli/hub)
