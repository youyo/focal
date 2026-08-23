# 0006. E2E テストは build tag 隔離 + docker compose + go-sdk クライアントで構成し CI 別ジョブで実行する

- 日付: 2026-08-24
- ステータス: 採用

## コンテキスト
focal は system ssh を介して実ホストを検査する CLI + Remote MCP ツールであり、単体テスト(mock Executor による golden argv 検証等)だけでは「実際に SSH 接続して operation が通るか」「MCP プロトコル(spec 2026-07-28 stateless Streamable HTTP)経由でツール呼び出しが成立するか」を保証できない。手動でコンテナを立てて検証していたが、再現性が無く回帰検知にならない。これを自動化しつつ、通常の `go build/test/lint` や開発体験を損なわない形にする必要があった。

## 決定
- E2E テストは `test/e2e/` に置き、全ファイル冒頭に `//go:build e2e` タグを付ける。通常の `go test ./...` の対象外とし、build/test/lint ジョブを汚さない。
- sshd は `test/e2e/docker-compose.yml` + `Dockerfile` で定義し、`TestMain` が up/down・鍵生成・focal build・専用 known_hosts を管理する。docker/docker compose 不在時は `t.Skip` でローカル開発を壊さない。
- CLI 経路は focal バイナリを os/exec で実行し JSON envelope を検証。MCP 経路は既存依存の go-sdk(modelcontextprotocol/go-sdk v1.7.0)のクライアントで focal serve に接続し、SSE / protocol-version / clientCapabilities / Mcp-* ヘッダーの処理を SDK に委ねる。
- 実行入口は `mise run e2e`(開発時)と GitHub Actions の別ジョブ `e2e`(ubuntu-latest、docker プリインストール)。既存の build/test/lint ジョブは不変。
- focal 本体は非改変。known_hosts 検証は PATH 先頭に置いた薄い ssh ラッパで `-o UserKnownHostsFile` 等を注入する。

## 検討した代替案
- **ory/dockertest 等のライブラリ導入**: 新規依存になる。focal は依存最小方針であり、go-sdk と docker compose CLI で足りるため却下。
- **MCP クライアントを raw JSON-RPC で自作**: SSE 応答形式・必須ヘッダー・clientCapabilities 等を手で組む必要があり脆い。既存依存の go-sdk クライアントで代替できるため却下。
- **CI に組み込まず mise task のみ**: 回帰検知が開発者の手動実行頼みになる。GitHub Actions の ubuntu runner は docker がネイティブに使えるため別ジョブとして成立し、無理筋ではないと判断して採用。

## 影響
- 実 SSH・実 MCP の回帰が PR/main push ごとに自動検知される(CI 実行時間は e2e ジョブ分だけ増加、約 1 分)。
- ローカルでは docker が必要。AF_UNIX bind を拒否する sandbox 環境では UDS 系テストがローカル fail するため、その裏取りは CI に依存する(ADR 外の運用メモだが E2E 前提として関連)。
- 新規依存はゼロ。E2E は build tag で隔離されるため、E2E を持たない環境・ツールチェーンに影響しない。
