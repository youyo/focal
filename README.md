# Focal

**Focal never accepts arbitrary remote commands.**

Focal exposes a fixed set of structured inspection operations and executes them through your existing OpenSSH configuration.

Focal は Coding Agent 向けの制限付き SSH 検査ツールです。Agent が制御できるのは「どの host を見るか」「どの built-in operation を使うか」「その operation が明示的に公開した型付き parameter」だけで、実際の Linux コマンドは一切制御できません。出力は Coding Agent が消費しやすい構造化 JSON を既定とします。

## Development

Runtime とタスクは [mise](https://mise.jdx.dev/) で管理します。

```sh
mise install      # Go などのツールを導入
mise run build    # go build ./...
mise run test     # go test ./...
mise run lint     # golangci-lint run ./...
mise run fmt      # gofmt -w .
```

ロードマップは [GitHub Project](https://github.com/users/youyo/projects/2) と Milestones を参照してください。
