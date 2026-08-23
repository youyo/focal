# 0005. MCP SDK に modelcontextprotocol/go-sdk を採用し stateless Streamable HTTP で提供する

## ステータス

Accepted

## コンテキスト

`internal/mcp`（issue #14）は Focal の operation を Remote MCP server の tool として公開する。
実装にあたり、次の2つを決める必要があった。

1. MCP プロトコルを Go でどう実装するか（標準ライブラリでの自前実装 / サードパーティ SDK の採用、
   採用する場合はどの SDK か）。
2. MCP の輸送方式として、セッションを持つ通常の Streamable HTTP と、セッションを持たない
   stateless Streamable HTTP のどちらをターゲットにするか。

Focal の tool 入力は operation ごとに型（`hostArgs` を埋め込んだ `serviceArgs` / `processesArgs` /
`logsArgs` / `kernelArgs` 等、`internal/mcp/tools.go`）で表現されており、JSON Schema への変換・
受信した引数のスキーマ検証・`tools/list` や `tools/call` などの JSON-RPC メッセージの取り扱いを
Focal 自身で持つか、SDK に委ねるかが実装コストと安全性の両面で効いてくる。issue #14 の追記が
「MCP spec `2026-07-28`（stateless Streamable HTTP）をターゲットにする」と述べているとおり、
2026-07-28 revision で protocol core が stateless になったため、通常の HTTP サービスとして
Remote MCP を立てられるようになったことも前提にある。

## 決定

MCP プロトコル実装には `github.com/modelcontextprotocol/go-sdk`（v1.7.0、Model Context Protocol
公式の Go SDK）を採用する。`internal/mcp/tools.go` は operation ごとの引数の型（Go の struct）だけを
宣言し、SDK の `sdk.AddTool` にその型を渡すことで JSON Schema の生成・受信引数のスキーマ検証・
`tools/list` / `tools/call` の JSON-RPC ハンドリングを SDK に委ねる。Focal 側が書くのは
「どの型を受け付けるか」（struct のフィールドと `jsonschema` タグ）と、検証済みの値を
`operation.Params` に写す関数だけであり、JSON-RPC メッセージそのものを Focal がパースする経路は
持たない。

輸送方式は 2026-07-28 revision の stateless Streamable HTTP をターゲットにする。`initialize`
ハンドシェイクとセッション ID を持たず、POST リクエストごとに自己完結して処理する
（`sdk.NewStreamableHTTPHandler` に `StreamableHTTPOptions{Stateless: true}` を渡す。
`internal/mcp/server.go`）。これにより `focal serve` は状態を持たない通常の HTTP サービスとして
動作し、[idproxy](https://github.com/youyo/idproxy) のような認証プロキシをそのまま前段に置ける
（issue #18）。

## 検討した代替案

- **標準ライブラリ（`net/http` + `encoding/json`）での自前実装**: 依存を1つ減らせるが、JSON-RPC
  メッセージのパース・`tools/list` / `tools/call` のディスパッチ・JSON Schema の生成と検証を
  Focal 自身が保守することになる。Focal の価値は「operation の型を厳格に守ること」にあり、
  MCP プロトコル自体の正しさを担保する作業はその価値に直結しない上、プロトコル改定（2026-07-28
  revision のような）に追従するコストを継続的に負うことになる。不採用。
- **`mark3labs/mcp-go`**: 別のサードパーティ SDK。stateless Streamable HTTP（2026-07-28 revision）への
  対応状況が modelcontextprotocol/go-sdk（公式）より遅れており、公式 SDK が Go の型からの JSON Schema
  推論・スキーマ検証を標準機能として持つのに対し、同等の機能を得るために追加の実装が必要になる。
  プロトコルの正本を管理する Model Context Protocol 自身が公開している SDK ではない点も、
  プロトコル改定への追従の速さという観点で公式 SDK に劣る。不採用。

## 影響

- `internal/mcp` の tool 入力スキーマは Go の型定義（`internal/mcp/tools.go` の `hostArgs` /
  `serviceArgs` 等）が単一の正本になる。新しい operation を追加する際は、型を1つ追加し
  `toolSpecs` に1行足すだけで、JSON Schema・検証・`operation.Params` への変換までが揃う
  （`tools_test.go` がこの対応関係のドリフトを検査する）。
- `focal serve` は `initialize` ハンドシェイクや `Mcp-Session-Id` を持たない。クライアント側が
  従来の（stateful な）Streamable HTTP のみをサポートする場合、2026-07-28 revision の stateless
  transport に対応していなければ接続できない可能性がある。
- stateless であることは `focal serve` が水平スケール可能な通常の HTTP サービスとして扱えることを
  意味し、認証プロキシ（idproxy）をロードバランサ配下に複数立てる構成とも相性がよい。
- `go-sdk` のメジャーバージョンアップやプロトコル revision の追加対応は、Model Context Protocol
  自身のリリースサイクルに追従する形になる。
