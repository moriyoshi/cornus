# Output modes

Every line the CLI prints flows through one output driver with three rendering modes, chosen by the global `--output` flag (env `CORNUS_OUTPUT`, default `auto`) — so the choice applies uniformly to every subcommand.

| `--output` | Rendering |
| --- | --- |
| `auto` (default) | fancy on an interactive terminal, plain otherwise |
| `fancy` | color, aligned tables, live spinners and progress bars |
| `plain` | deterministic, ANSI-free text — for pipes, CI, and logs |
| `json` | machine-readable NDJSON, one JSON object per line |

Auto-detection is conservative: fancy turns on only when both stdout and stderr are terminals, so `cornus compose ps | cat` never receives ANSI escapes. On Windows it stays plain unless color is forced. `--output json` is always colorless regardless of the terminal.

## Channel discipline

Channel discipline holds in every mode: a command's result — tables, values, and the log stream that is the point of `logs` — goes to stdout, while progress and notices go to stderr. So stdout stays pipe-clean, and a consumer of `--output json` reads structured results on stdout and structured progress on stderr.

## Color

Color follows the usual conventions:

* `--no-color` (or `NO_COLOR` / `CLICOLOR=0`) drops color while keeping the fancy layout.
* `CLICOLOR_FORCE` forces color on.

## Fancy mode

On an interactive terminal, fancy mode adds colored notice glyphs (`✓` / `▸` / `•` / `⚠` / `✗`), minimal header-underline tables, and per-service log prefixes colored by a stable hash of the service name (docker-compose style). A live progress region draws on stderr above the scrolling output: BuildKit-style per-step spinners for `cornus build`, and a per-service reconcile spinner with an overall bar for `cornus compose up` / `cornus deploy`. The region never touches stdin (interactive prompts still work) and only animates when stderr is a real terminal, so it can never corrupt a pipe.

## JSON mode (for coding agents)

`--output json` (or `CORNUS_OUTPUT=json`) emits newline-delimited JSON — one object per line, nothing else — so a coding agent or a script can consume Cornus's output without screen-scraping. Read both streams: results on stdout, progress and notices on stderr.

The object shapes:

* **Notices** (stderr): `{"level":"info","msg":"..."}`, where `level` is one of `step` / `done` / `success` / `info` / `warning` / `error`.
* **Log lines** (e.g. `cornus compose logs`, stdout): `{"type":"log","tag":"web","line":"...\n"}`.
* **Table rows** (e.g. `config get-contexts`, stdout): one object per row keyed by column header, e.g. `{"CURRENT":"*","NAME":"prod","SERVER":"https://prod:8443"}`.
* **Single values** (e.g. `version`, `token`): `{"value":"..."}`.
* **Command results / events**, each command's structured record:
  * `cornus build` result (stdout): `{"event":"built","tag":"localhost:5000/app:v1","digest":"sha256:..."}`, with build progress on stderr as `{"vertex":"[2/5] RUN ...","status":"start"}` (`status` is `start` / `done` / `cached` / `error`) and verbatim `{"log":"...\n"}` lines.
  * `cornus deploy` (stdout): `{"event":"deployed","name":"app","running":2,"total":3}`.
  * `cornus compose up` / `down` (stderr events): `{"service":"web","event":"up","running":2,"total":2}` — the `event` verb is one of `up`, `removed`, `forwarding`, `started`, `stopped`, `restarted`, `recreated`, `transition`, ....
  * `cornus tunnel` (stdout): `{"event":"tunnel","name":"app","port":8080,"url":"https://....ngrok...."}`.
  * `cornus daemon status` (stdout): `{"running":true,"servers":[...],"projects":{...}}`.

```sh
# Drive a build and pull out the pushed digest:
cornus --output json build -t localhost:5000/app:v1 . 2>/dev/null \
  | jq -r 'select(.event=="built") | .digest'

# Stream compose lifecycle events as NDJSON (results on stdout, events on stderr):
CORNUS_OUTPUT=json cornus compose up

# List connection profiles, one JSON object per row:
cornus --output json config get-contexts | jq -r .NAME
```

::: tip
Use `plain` for CI logs and pipelines, and `json` when a coding agent or script needs to parse Cornus's output deterministically.
:::
