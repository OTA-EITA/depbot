# depbot

[![ci](https://github.com/OTA-EITA/depbot/actions/workflows/ci.yml/badge.svg)](https://github.com/OTA-EITA/depbot/actions/workflows/ci.yml)

Dependabot のように、依存関係の新しいバージョンを見つけて**依存ごとに PR を作る** Go 製の CLI / GitHub Action です。
Go モジュール、GitHub Actions、Dockerfile のベースイメージに対応し、GitHub Actions を**コミット SHA に固定**することもできます。

A Dependabot-like CLI and GitHub Action that finds newer versions of Go modules, GitHub Actions and
Dockerfile base images, and opens one pull request per update. It can also pin actions to commit SHAs.

## 特徴

| | 内容 |
|---|---|
| Go モジュール | Go のモジュールプロキシ（`$GOPROXY`）で直接依存の最新版を調べ、`go get` と `go mod tidy` で go.sum まで更新。メジャーバージョンはモジュールパスが変わるため同じメジャー内で更新。更新先がより新しい Go を要求する場合は、Dependabot と同じく `go` ディレクティブも上がります（`GOTOOLCHAIN=auto` で実行。`DEPBOT_GOTOOLCHAIN` で変更可能） |
| GitHub Actions | `uses:` のタグを最新に更新（`v4` → `v5` のように書き方の粒度を保つ）。SHA 固定（`@<sha> # v4.2.2`）されたものは SHA とコメントの両方を更新 |
| Dockerfile | `FROM` のタグを、形式を保って更新（`golang:1.24-alpine` → `golang:1.25-alpine`）。ビルドステージ、`${ARG}`、ダイジェスト固定、`latest` は対象外。Docker Hub / GHCR / GCR / registry.k8s.io など OCI 準拠のレジストリに匿名トークンで対応 |
| `--pin` | `uses: actions/checkout@v4` を `uses: actions/checkout@<sha> # v4` に変換。タグの付け替えやすり替えによるサプライチェーン攻撃を防ぐ（GitHub のセキュリティ強化ガイドの推奨） |
| PR の作り方 | GitHub の Git Data API でコミットとブランチを作るため、ローカルの git 操作が不要。同じ依存の同じバージョンへの更新は 1 つの PR にまとめ、既に開いている PR は作り直さない |

## 使い方

```sh
# 更新できる依存を一覧表示
depbot check

# 作業ツリーに適用
depbot update

# GitHub Actions を SHA に固定
depbot update --ecosystem github-actions --pin

# 依存ごとに PR を作成（GITHUB_TOKEN が必要）。まずは --dry-run で確認
depbot pr --repo OTA-EITA/my-app --dry-run
depbot pr --repo OTA-EITA/my-app
```

出力例（[`testdata/sample`](testdata/sample) に対する実行結果）:

```
ECOSYSTEM       DEPENDENCY         FROM         TO           KIND   WHERE
docker          alpine             3.18         3.24         minor  Dockerfile:6
docker          golang             1.22-alpine  1.27-alpine  minor  Dockerfile:1
github-actions  actions/checkout   v3           v7           major  .github/workflows/ci.yml:7
github-actions  actions/setup-go   v4           v7           major  .github/workflows/ci.yml:8
gomod           golang.org/x/text  v0.20.0      v0.43.0      minor  .
```

| フラグ | 既定値 | 説明 |
|---|---|---|
| `--ecosystem` | `gomod,github-actions,docker` | 対象のエコシステム |
| `--ignore` | - | 対象外にする依存（`*` でワイルドカード、複数指定可） |
| `--pin` | `false` | GitHub Actions を SHA に固定 |
| `--output`（check） | `table` | `table` / `json` / `markdown` |
| `--exit-code`（check） | `false` | 更新があれば終了コード 1（CI で古い依存を検知） |
| `--repo` / `--base` / `--dry-run`（pr） | `$GITHUB_REPOSITORY` / `main` / `false` | PR の作成先 |

## GitHub Actions で定期実行する

```yaml
name: depbot
on:
  schedule:
    - cron: "0 0 * * 1"   # 毎週月曜
  workflow_dispatch:

permissions:
  contents: write
  pull-requests: write

jobs:
  update:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v5
      - uses: OTA-EITA/depbot@v0.1.0
        with:
          pin: "true"
```

- リポジトリの Settings → Actions → General で「Allow GitHub Actions to create and approve pull requests」を有効にしてください。
- 既定の `GITHUB_TOKEN` には GitHub の仕様で 2 つの制約があります。`.github/workflows/` 配下のファイルを変更できない（GitHub Actions の更新・SHA 固定の PR は作れず、depbot はその旨を表示してほかの更新を続けます）ことと、作った PR で CI が自動で走らないことです。どちらも、`token` に workflows 権限を持つ GitHub App のトークンか fine-grained PAT を渡せば解消します。

このリポジトリ自身も [`.github/workflows/depbot.yml`](.github/workflows/depbot.yml) で depbot に依存を更新させています。

## インストール

[Releases](https://github.com/OTA-EITA/depbot/releases) からバイナリをダウンロードするか、Go でインストールします。

```sh
go install github.com/OTA-EITA/depbot/cmd/depbot@latest
```

## テスト

- エコシステムごとに、モジュールプロキシ・GitHub API・レジストリ（トークン認証とページングを含む）を httptest で再現したテスト
- PR 作成の Git Data API の呼び出し順（blob → tree → commit → ref → PR、既存ブランチの付け替え）のテスト
- CI の E2E: わざと古い依存を持つ [`testdata/sample`](testdata/sample) に対し、本物の Go プロキシ・GitHub API・Docker Hub で更新を検出 → 適用後にビルドが通ること → SHA 固定 → PR のドライランまでを確認（[`e2e/run.sh`](e2e/run.sh)）

## License

MIT
