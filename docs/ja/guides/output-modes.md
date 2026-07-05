# 出力モード

CLI の出力はすべて 1 つの出力ドライバーを通り、グローバルな `--output` フラグ (環境変数は `CORNUS_OUTPUT`、既定値は `auto`) で選んだ四つの表示モードのいずれかで表示されます。そのため、この設定はすべてのサブコマンドに一貫して適用されます。

| `--output` | レンダリング |
| --- | --- |
| `auto` (既定) | 対話的な端末では装飾付き、それ以外ではプレーン |
| `fancy` | 色、整列した表、ライブスピナーと進捗バー |
| `plain` | 再現可能で ANSI を含まないテキスト。パイプ、CI、ログ向け |
| `json` | 機械可読な NDJSON。1 行につき JSON オブジェクト一つ |

自動検出は保守的です。stdout と stderr の両方が端末である場合にのみ fancy が有効になるため、`cornus compose ps | cat` に ANSI エスケープが流れ込むことはありません。Windows では色を強制しない限りプレーンのままです。`--output json` は端末の状態にかかわらず常に無色です。

## 出力チャネルの使い分け

どのモードでも出力チャネルの使い分けは同じです。コマンドの結果 (表、値、そして `logs` の主目的であるログストリーム) は stdout に、進捗と通知は stderr に出力されます。これにより stdout はパイプに安全に渡せ、`--output json` の利用者は stdout から構造化された結果を、stderr から構造化された進捗を読み取れます。

## 色

色は一般的な慣例に従います。

* `--no-color` (`NO_COLOR` / `CLICOLOR=0` でも可) は fancy のレイアウトを保ったまま色を無効にします。
* `CLICOLOR_FORCE` は色を強制的に有効にします。

## Fancy モード

対話的な端末では、fancy モードは色付きの通知記号 (`✓` / `▸` / `•` / `⚠` / `✗`)、控えめな見出し下線付きの表、サービス名の安定ハッシュで色付けしたサービス別ログプレフィックス (docker-compose 風) を追加します。ライブ進捗領域はスクロール中の出力の上に stderr へ描画されます。`cornus build` では BuildKit 風のステップごとのスピナー、`cornus compose up` / `cornus deploy` ではサービスごとの収束処理を示すスピナーと全体バーが表示されます。この領域は stdin に触れないため対話プロンプトは機能し続け、stderr が実際の端末であるときだけアニメーションするため、パイプを壊すこともありません。

## JSON モード (コーディングエージェント向け)

`--output json` (`CORNUS_OUTPUT=json` でも可) は改行区切り JSON を出力します。1 行につきオブジェクト一つだけで、ほかの出力はありません。そのためコーディングエージェントやスクリプトは画面の表示を解析せずに Cornus の出力を利用できます。両方のストリームを読んでください。結果は stdout、進捗と通知は stderr です。

オブジェクトの形式は次のとおりです。

* **通知** (stderr): `{"level":"info","msg":"..."}`。`level` は `step` / `done` / `success` / `info` / `warning` / `error` のいずれかです。
* **ログ行** (例: `cornus compose logs`、stdout): `{"type":"log","tag":"web","line":"...\n"}`。
* **表の行** (例: `config get-contexts`、stdout): 列見出しをキーとする行ごとのオブジェクト。例: `{"CURRENT":"*","NAME":"prod","SERVER":"https://prod:8443"}`。
* **単一値** (例: `version`、`token`): `{"value":"..."}`。
* **コマンド結果 / イベント**。各コマンドの構造化レコード:
  * `cornus build` の結果 (stdout): `{"event":"built","tag":"localhost:5000/app:v1","digest":"sha256:..."}`。ビルド進捗は stderr に `{"vertex":"[2/5] RUN ...","status":"start"}` として出力されます (`status` は `start` / `done` / `cached` / `error`)。ログ行はそのまま `{"log":"...\n"}` として出力されます。
  * `cornus deploy` (stdout): `{"event":"deployed","name":"app","running":2,"total":3}`。
  * `cornus compose up` / `down` (stderr イベント): `{"service":"web","event":"up","running":2,"total":2}`。`event` の動詞は `up`、`removed`、`forwarding`、`started`、`stopped`、`restarted`、`recreated`、`transition` などです。
  * `cornus tunnel` (stdout): `{"event":"tunnel","name":"app","port":8080,"url":"https://....ngrok...."}`。
  * `cornus daemon status` (stdout): `{"running":true,"servers":[...],"projects":{...}}`。

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
CI のログとパイプラインには `plain` を、コーディングエージェントやスクリプトが Cornus の出力を決定的に解析する必要がある場合には `json` を使ってください。
:::
