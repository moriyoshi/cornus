# cornus version / cornus health

Cornus のバージョンを表示するか、実行中サーバーのヘルスエンドポイントを確認します。

## cornus version

cornus のバージョンを表示します。

### 構文

```sh
cornus version
```

### 説明

`cornus version` はバイナリのバージョン文字列を表示します。ビルド時に `-ldflags "-X main.version=..."` で上書きでき、既定値は `dev` です。

### 例

```sh
cornus version
```

## cornus health

Cornus サーバーの `/healthz` エンドポイントを確認し、正常でなければ非ゼロで終了します。

### 構文

```sh
cornus health [flags]
```

### 説明

`cornus health` は 5 秒のタイムアウトで `http://<addr>/healthz` に HTTP `GET` を送ります。サーバーが `200 OK` を返さない限り非ゼロで終了します。コンテナのヘルスチェック用なので、イメージに `curl` などの追加ツールは必要ありません。

### フラグ

| フラグ | 環境変数 | 既定値 | 説明 |
| --- | --- | --- | --- |
| `--addr` | — | `127.0.0.1:5000` | 確認するサーバーアドレス。 |

### 例

既定のローカルアドレスを確認します。

```sh
cornus health
```

特定のアドレスを確認します。

```sh
cornus health --addr 127.0.0.1:8080
```

コンテナヘルスチェックとして使用します (Dockerfile)。

```dockerfile
HEALTHCHECK CMD ["cornus", "health", "--addr", "127.0.0.1:5000"]
```
