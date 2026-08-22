# Focal

> Give coding agents visibility, not shell access.

**Focal never accepts arbitrary remote commands.**

Focal exposes a fixed set of structured inspection operations and executes them through your existing OpenSSH configuration. Focal does not replace SSH configuration — it constrains what may be executed through SSH.

Focal は Coding Agent 向けの制限付き SSH 検査ツールです。Agent が制御できるのは「どの host を見るか」「どの built-in operation を使うか」「その operation が明示的に公開した型付き parameter」だけで、実際の Linux コマンドは一切制御できません。出力は Coding Agent が消費しやすい構造化 JSON を既定とします。

## Requirements

Focal は接続先への SSH 実行に、利用者の環境にインストール済みの OpenSSH クライアントをそのまま使います。**OpenSSH クライアント 10.1 以降を推奨します**(CVE-2023-51385 / CVE-2025-61984 は `ssh_config` の `%h` / `%u` 展開経由の間接的な injection 経路であり、最新の OpenSSH クライアントを使うことが多層防御の一段になります)。

## Development

Runtime とタスクは [mise](https://mise.jdx.dev/) で管理します。

```sh
mise install      # Go などのツールを導入
mise run build    # go build ./...
mise run test     # go test ./...
mise run lint     # golangci-lint run ./...
mise run fmt      # gofmt -w .
```

CI (`.github/workflows/ci.yml`) も `jdx/mise-action` 経由でこれと同じ `mise run build` / `mise run test` / `mise run lint` を実行し、ローカルと CI のタスク定義を乖離させません。

ロードマップは [GitHub Project](https://github.com/users/youyo/projects/2) と Milestones を参照してください。
