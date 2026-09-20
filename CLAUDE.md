# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

Focal は Coding Agent 向けの「制限付き SSH 検査ツール」。任意リモートコマンドは一切受け付けず、
固定された built-in operation だけを OpenSSH 経由で実行する。詳しい仕様・operation 一覧・CLI/MCP
フラグは README.md を参照。

## コマンド

Runtime とタスクは mise 管理（Go 1.26 / golangci-lint 2.13.1）。CI も同じ mise タスクを実行する。

```sh
mise run build      # go build ./...
mise run test       # go test ./...
mise run test-race  # go test -race ./...
mise run lint       # golangci-lint run ./...
mise run fmt        # gofmt -w .
mise run e2e        # go test -tags=e2e -count=1 ./test/e2e/...（docker + docker compose が必要）
```

単一テストは go test を直接叩く:

```sh
go test -run 'TestOnlyAllowedExportedFunctionsReturnCommand' ./internal/ssh/
go test -tags=e2e -count=1 -run 'TestCLIInspections' ./test/e2e/
```

## アーキテクチャ

### リクエストの一本道

CLI (`internal/cli`) と MCP (`internal/mcp`) は同じ経路を通る。分岐や近道はない。

1. `operation.Registry.Build(name, params)` — 名前から Operation を作る唯一のテーブル
   (`internal/operation/registry.go` の `registryTable`)。他パッケージが Operation の Go 型を
   直接名指しして構築することはない。
2. `internal/vo` の `Parse*` — 呼び出し元由来の文字列はここを必ず通る（`ServiceName` /
   `Duration` / `LineLimit` / `PID` / `ProcessName` / `Target`）。
3. `policy.Resolve(name, configured)` — operation ごとの capability（`internal/policy/capability.go`、
   Focal 自身のコード）と設定ファイルの権限昇格モードの交差。config は capability を**狭めることしか
   できず**、範囲外指定は黙って弱い設定に落ちるのではなく起動時エラーになる。
4. `internal/ssh` の exported factory → `newCommand` — remote argv が組み立てられる唯一の場所。
   `Command` はフィールド非公開・`newCommand` が唯一のコンストラクタで、`policy.Policy` を受け取る
   ため権限昇格モードは構築時に焼き込まれる。
5. `ssh.Executor.Execute` — 実行の唯一の場所。権限昇格の再判断はしない（`Command.Sudo()` だけを見る）。
   `vo.Transport`（`--transport` / tool call の `transport`、既定 `ssh`）で `ssh.OpenSSH` と
   `internal/ssm.Executor`（AWS SSM Run Command、`execution.transports` で明示的に有効化しない限り拒否）
   のどちらの `ssh.Executor` 実装を使うかが決まるだけで、`Command` の生成経路・内容には一切影響しない
   （[ADR 0007](docs/adr/0007-ssm-transport.md)）。

argv の token は provenance で区別される（`lit` = Focal が書いた文字列リテラル、`val` = 呼び出し元の値、
`prefixed` = その連結）。`val` は先頭ハイフン・シェルメタ文字を拒否するので、呼び出し元の値がオプションに
化けることはない。

出力は CLI/MCP とも `result.Envelope` に統一。composite operation（`inspect`）は `Parts` に入れ子で畳む。

### ソースを検査するガードテスト（変更時に必ず影響する）

このリポジトリには go/ast でソースコード自体を読むテストがある。境界を広げる変更は複数箇所の
手書きテーブル更新を強制される。これが設計の要なので、テストが落ちたら「テーブルを直す」のではなく
「本当に境界を広げてよいか」を先に考えること。

- `internal/ssh/boundary_test.go` — `Command` を返す exported 関数が `allowedCommandFactories` と
  一致するか、`lit` の引数が文字列リテラルか、factory が `policy.Policy` を取るか等を検査。
- `internal/security_test.go` — 上とは**独立に**同じ catalogue を外側から再発見するクロスパッケージ
  回帰テスト。command factory を追加したら boundary_test 側と security_test 側の両方のテーブルに
  現実的な入力付きで登録しないと通らない。
- `internal/operation/registry_test.go` — `registryTable` と `internal/policy` の capability 宣言の
  drift を検出。operation を追加するときは capability 行 → registry 行 → 両ガードテストの順。

### テスト方針

- SSH 層より上（operation / CLI / MCP）は `internal/sshtest.Recorder` を使い実機なしで検証する。
  受け取った `Command` を assert することが、factory と `policy.Resolve` を通った証明になる。
- golden file: `internal/ssh/testdata/argv_golden.txt`（実際にリモートへ渡る argv の全記録）、
  `internal/result/testdata/*.json`、`internal/config/testdata/*.yaml`。
- `test/e2e` は `e2e` build tag 付きで実 sshd コンテナに対して実行（`docs/adr/0006`）。

### その他

- 設定ファイルは未知キー・範囲外の値をすべて起動時エラーにする（`internal/config`）。安全側デフォルトは
  「read-only operation は全有効、`kernel` のみ無効、権限昇格はどこにも掛からない」。
- `~/.ssh/config` の解釈は CLI・MCP とも OpenSSH に完全委任する（ADR 0001 / 0004）。Focal 側で接続先を
  再定義したり `-o` 相当を通したりする経路は作らない。
- 設計判断は `docs/adr/` に記録する。境界に関わる変更をするときは 0002（Command 生成境界）を先に読む。
