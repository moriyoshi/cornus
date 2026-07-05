# cornus auth

短時間有効な Cornus クライアントセッションの発行に使う SSH 公開鍵を登録し、管理します。秘密鍵はクライアントに残り、サーバーが保存するのは公開鍵だけです。

## 概要

```sh
cornus auth enrollment-code
cornus auth enroll [flags]
cornus auth token [flags]
cornus auth keys [flags]
cornus auth delete-key <SHA256-fingerprint> [flags]
```

## 登録

サーバーで `CORNUS_AUTH_KEYSTORE=file` または `CORNUS_AUTHORIZED_KEYS` を設定して鍵認証を有効にします。サーバーの既定の状態は変わりません。どちらも設定されておらず、既存のキーストアもない場合、これらのルートは登録されません。

`cornus auth enrollment-code` はローカルコマンドです。サーバーを呼び出さずに `<CORNUS_DATA>/auth/enrollment.secret` を読み取るため、サーバーユーザーとして、または `ssh`、`docker exec`、`kubectl exec` 経由で実行してください。登録が成功するたびにコードはローテーションされます。

```sh
# サーバーホスト上:
code=$(cornus auth enrollment-code)

# クライアント上:
cornus auth enroll --server https://cornus.example \
  --identity-file ~/.ssh/id_ed25519 --name laptop --code "$code"
```

`--identity-file` の代わりに `--key-fingerprint SHA256:...` を使うと、`SSH_AUTH_SOCK` 内の鍵を選択できます。RSA の証明には常に SHA-2 を使い、従来の RSA/SHA-1 署名は拒否します。

## 接続プロファイル

`key-auth` プロファイルが保存するのは秘密鍵ファイルのパスまたは ssh-agent のフィンガープリントであり、秘密鍵の内容やセッショントークンではありません。

```sh
cornus config set-context prod \
  --server https://cornus.example \
  --key-auth-identity-file ~/.ssh/id_ed25519 \
  --key-auth-name laptop
cornus config use-context prod
```

通常のコマンドは鍵の所有を証明し、短時間有効な bearer セッションを自動的に使います。資格情報の優先順位は `CORNUS_TOKEN`、`key-auth`、`kube-auth`、静的プロファイルの `token` の順です。1 つのプロファイルに `key-auth` と `kube-auth` の両方を設定することはできません。

セッションの既定値は `api` スコープと 1 時間の有効期間です (最長 24 時間)。セッションはランタイムディレクトリに非公開でキャッシュされ、有効期限の 2 分前に期限切れとして扱われます。フォアグラウンドコマンドはキャッシュを発行して更新し、バックグラウンドのクライアントエージェントは読み取りだけを行います。

## トークンと鍵の管理

`cornus auth token` はセッションを明示的に発行して表示します。`cornus auth keys` は認可済み公開鍵を一覧表示し、`cornus auth delete-key` は実行時に登録された鍵を削除します。一覧表示と削除には全権限の資格情報が必要です。

```sh
cornus auth token --identity-file ~/.ssh/id_ed25519 --scope api --ttl 30m
cornus auth keys
cornus auth delete-key SHA256:abc123...
```

`CORNUS_AUTHORIZED_KEYS` から供給した鍵は宣言的であり、API からは削除できません。鍵を削除すると新しいセッションは発行できなくなりますが、すでに発行済みのステートレス JWT は短い有効期限まで有効です。

**関連項目:** [セキュリティと認証](/ja/guides/security)、[cornus config](/ja/cli/config)、[接続設定](/ja/reference/connection-config)
