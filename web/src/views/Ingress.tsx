import { Show, For } from "solid-js";
import type { ClientIngress, Config } from "../api";

// IngressSection describes the ingress SETTINGS a session is running under — not
// the live routes, which belong to the workloads that own them.
//
// It is two independent halves, and that is the whole point of giving ingress a
// section instead of a card row. The FRONT DOOR is what the server advertises: the
// domain and class it stamps on Ingress objects, where an emulated front door is
// bound, which controller Service a native passthrough targets. THIS CLIENT is how
// the local conduit realizes ingress, which the server neither sets nor knows. A
// request to an ingress host traverses both, so a reader who sees only one of them
// cannot predict what happens to it — and the Server card, whose value column is
// ~150px, could fit neither in full: it carried a mode chip and a domain, and
// dropped the class, the listen address and the controller for want of room.
//
// Rendered unconditionally, like the Conduit card and for the same reason: a
// section that disappears when it has nothing to report leaves a reader unable to
// tell "nothing is configured" from "this dashboard never had that section". Every
// absence below therefore renders as a sentence naming what is absent, and none of
// them renders as a claim — a server that advertised no front door is not a server
// without one.
export default function IngressSection(props: { config?: Config }) {
  const server = () => props.config?.server?.ingress;
  const client = () => props.config?.clientIngress ?? [];
  // Only meaningful once the config resource has answered; until then the page
  // says "loading…" rather than picking one of the two empty sentences, either of
  // which would be a guess.
  const loaded = () => !!props.config;

  return (
    <section id="ingress" class="section">
      <div class="row">
        <h2 style={{ margin: 0 }}>Ingress</h2>
      </div>

      <h3>Front door</h3>
      <Show when={loaded()} fallback={<p class="muted">loading…</p>}>
        <Show
          when={server()}
          fallback={
            <p class="muted">
              This server advertised no ingress front door — a host backend with none to
              offer, or a cluster lookup that did not find one.
            </p>
          }
        >
          {(ing) => (
            <dl class="kv">
              <dt>Mode</dt>
              <dd>
                <span
                  class="badge"
                  title={
                    ing().emulated
                      ? "This server realizes ingress itself, routing to workloads with its own host/path table."
                      : "Ingress is realized by the cluster's own ingress controller."
                  }
                >
                  {ing().emulated ? "emulated" : "cluster"}
                </span>
              </dd>

              {/* The base domain predicts the host a deploy will be given, which is
                  the one thing a reader is most often here to work out. */}
              <dt>Base domain</dt>
              <dd>{ing().domain || <span class="muted">—</span>}</dd>

              {/* The operator's server default, applied to every Ingress this server
                  creates. It rode on the wire unrendered while ingress lived in the
                  Server card, purely because a third item wrapped that card's value
                  column onto a second line. */}
              <dt>Class</dt>
              <dd>{ing().class || <span class="muted">—</span>}</dd>

              {/* Emulated only: where the server's own front door is bound. Empty is
                  a real, distinct state — the front door exists but is reachable only
                  through an ingress tunnel — so it gets a sentence, not a dash. */}
              <Show when={ing().emulated}>
                <dt>Listen</dt>
                <dd>
                  <Show
                    when={ing().listen}
                    fallback={
                      <span class="muted" title="CORNUS_INGRESS_LISTEN is unset on this server.">
                        not bound; reachable through an ingress tunnel only
                      </span>
                    }
                  >
                    {(l) => <>{l()}</>}
                  </Show>
                </dd>
              </Show>

              {/* The controller Service a native passthrough port-forwards to. Absent
                  whenever the server could not discover one, which is why a client
                  falls back to emulation — so it is named, never silently omitted. */}
              <dt>Controller</dt>
              <dd>
                <Show
                  when={ing().controller}
                  fallback={
                    <span
                      class="muted"
                      title="No in-cluster ingress controller Service was discovered, so a client cannot passthrough natively and falls back to emulating ingress itself."
                    >
                      none discovered
                    </span>
                  }
                >
                  {(c) => (
                    <span class="row">
                      <span>
                        {c().namespace ? `${c().namespace}/` : ""}
                        {c().service || <span class="muted">unnamed</span>}
                      </span>
                      <Show when={c().http_port}>
                        <span class="muted" title="HTTP port">
                          :{c().http_port}
                        </span>
                      </Show>
                      <Show when={c().https_port}>
                        <span class="muted" title="HTTPS port">
                          :{c().https_port}
                        </span>
                      </Show>
                    </span>
                  )}
                </Show>
              </dd>
            </dl>
          )}
        </Show>
      </Show>

      <h3>This client</h3>
      <Show when={loaded()} fallback={<p class="muted">loading…</p>}>
        <Show
          when={client().length}
          fallback={
            // The two empty states are DIFFERENT answers and must never collapse into
            // one sentence: with no agent there is nothing to read the setting from,
            // which is not the same as having read it and found nothing routed.
            <Show
              when={props.config?.agentLive}
              fallback={
                <p class="muted">
                  No client agent is running, so this client's ingress conduit settings are
                  unknown.
                </p>
              }
            >
              <p class="muted">This client routes no ingress through the conduit.</p>
            </Show>
          }
        >
          {/* Normally exactly one. More than one means this agent holds conduits with
              different ingress settings — worth showing as separate blocks rather
              than merged, because a workload reached through one of them is not
              reached through the other. */}
          <For each={client()}>{(c) => <ClientIngressFacts ingress={c} />}</For>
        </Show>
      </Show>
    </section>
  );
}

function ClientIngressFacts(props: { ingress: ClientIngress }) {
  const c = () => props.ingress;
  return (
    <dl class="kv">
      <dt>Mode</dt>
      <dd>
        <span
          class="badge"
          title={
            c().mode === "native"
              ? "This client tunnels straight to the cluster's own ingress controller, which does the Host/path routing and serves its own TLS."
              : "This client runs its own HTTP(S) reverse proxy and routes to workloads through the conduit."
          }
        >
          {c().mode}
        </span>
      </dd>

      <dt>Domain</dt>
      <dd>
        <Show
          when={c().domain}
          fallback={
            <span
              class="muted"
              title="No suffix was configured, so hosts are derived under the conduit's own service-host suffix."
            >
              conduit default
            </span>
          }
        >
          {(d) => <>{d()}</>}
        </Show>
      </dd>

      <Show when={c().controller}>
        {(ctl) => (
          <>
            <dt>Controller</dt>
            <dd>
              <span class="row">
                <span>
                  {ctl().namespace ? `${ctl().namespace}/` : ""}
                  {ctl().service || <span class="muted">unnamed</span>}
                </span>
                <Show when={ctl().kubeContext}>
                  <span class="muted" title="kubeconfig context used to reach it">
                    {ctl().kubeContext}
                  </span>
                </Show>
                <Show when={ctl().httpPort}>
                  <span class="muted" title="HTTP port">
                    :{ctl().httpPort}
                  </span>
                </Show>
                <Show when={ctl().httpsPort}>
                  <span class="muted" title="HTTPS port">
                    :{ctl().httpsPort}
                  </span>
                </Show>
              </span>
            </dd>
          </>
        )}
      </Show>

      {/* What a browser has to accept, in the conduit's own words. Emulate mode
          mints certificates the machine does not trust yet, so this is the row that
          turns a TLS error into an action. Native mode presents the real
          controller's certificate and reports nothing here. */}
      <Show when={c().trust?.length}>
        <dt>Trust</dt>
        <dd>
          <For each={c().trust}>{(t) => <p class="muted">{t}</p>}</For>
        </dd>
      </Show>
    </dl>
  );
}
