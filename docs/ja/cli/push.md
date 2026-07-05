# cornus push

イメージをレジストリへコピーします (たとえば Cornus 自身のレジストリへ)。

## 構文

```sh
cornus push <source> <dest> [flags]
```

## 説明

`cornus push` はイメージを宛先レジストリ参照へコピーします。ソースには別のレジストリ参照、またはローカルのイメージ tarball (OCI または docker-archive) を指定できます。`source` がディスク上に存在するファイルなら tarball として読み込みプッシュし、そうでなければレジストリ参照として解析・コピーします。

`CORNUS_TOKEN` が設定されている場合、Cornus はそのベアラートークンで宛先レジストリに認証します。トークンの対象は宛先レジストリホストだけなので、レジストリをまたぐコピーで関係のないソースレジストリへ Cornus のトークンを送ることはありません。

## フラグ

| フラグ | 環境変数 | 既定値 | 説明 |
| --- | --- | --- | --- |
| `source` (位置引数) | — | 必須 | ソース: レジストリ参照またはローカルイメージ tarball のパス。 |
| `dest` (位置引数) | — | 必須 | 宛先レジストリ参照。例: `localhost:5000/app:v1`。 |
| `--insecure` | — | `true` | HTTP (非 TLS) レジストリを許可します。 |

設定されている場合、`CORNUS_TOKEN` は宛先レジストリの認証に使う bearer トークンを提供します。

## 例

ローカル OCI tarball を cornus レジストリへプッシュします。

```sh
cornus push ./app.tar localhost:5000/app:v1
```

一つのレジストリから別のレジストリへイメージをコピーします。

```sh
cornus push docker.io/library/nginx:latest localhost:5000/nginx:latest
```

トークンで保護されたレジストリに認証します。

```sh
CORNUS_TOKEN=$(cornus token ...) cornus push ./app.tar registry.example.com/app:v1
```

## 関連項目

- [`cornus build`](/ja/cli/build)
- [`cornus token`](/ja/cli/token)
- [認証と TLS](/ja/topics/auth-and-tls)
