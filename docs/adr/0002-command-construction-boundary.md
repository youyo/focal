# 0002. Command 型の生成境界と sudo 判断の一点集約

## ステータス

Accepted

## コンテキスト

Focal の設計原則は "Focal never accepts arbitrary remote commands." であり、`internal/ssh.Command`
（`Program` / `Args` / `Sudo`）が実際にリモートへ渡る argv を表す唯一の型になる（issue #6）。
この型を外部パッケージが自由に生成できてしまうと、CLI/MCP からの入力が Operation → Policy →
SSH Executor の依存方向（issue #1）を経由せずに任意の argv を組み立てて Executor に渡せてしまい、
Command 型を security boundary として置いた意味が失われる。

もう一つ、sudo 実行可否は「operation capability（そもそも sudo を許すか、どのモードまで許すか）」と
「administrator が config.yaml で設定した sudo mode」の交差判定（issue #5）で決まる。この交差判定を
経由せずに `Command.Sudo` へ任意の `SudoMode` を焼き込める経路が1箇所でも残ると、config.yaml では
`sudo: never` に制限されているはずの operation が `sudo -n` 付きで実行される、という policy 迂回が
成立してしまう。

## 決定

### Command のフィールドを unexported にし、生成を ssh パッケージ内のファクトリに限定する

`ssh.Command` の `Program` / `Args` / `Sudo` はすべて unexported フィールドとする。外部パッケージからは
構造体リテラルで `ssh.Command{}` を組み立てられない。読み取りは `Program() string` /
`Args() []string`（防御的コピーを返す）/ `Sudo() policy.SudoMode` / `IsZero() bool` のみを公開する。
生成手段は `internal/ssh` パッケージ内の unexported コンストラクタ
`newCommand(p policy.Policy, program string, args ...string) (Command, *result.Error)` と、
operation ごとに公開する exported ファクトリ（M2 時点では `UptimeCommand(p policy.Policy) (Command, *result.Error)`
のみ）に限定する。ファクトリは `policy.Policy` を受け取り、`Command.sudo` にはその `Policy.Sudo()` を
そのまま焼き込む。

### sudo 判断は `policy.Resolve` の一点に集約する（sudo_contract）

sudo の実効値は `policy.Resolve(name string, configured policy.SudoMode) (policy.Policy, *result.Error)`
が返す `policy.Policy` 一点でのみ決まる。`policy.Policy` のフィールドは unexported とし、
アクセサは `Operation() string` と `Sudo() policy.SudoMode` のみを公開する。`Resolve` 以外に
`Policy` を生成する手段を持たない（exported フィールドがないため外部パッケージは struct literal で
組み立てられない）。ゼロ値は `operation` 空文字 + `SudoNever` であり、`Resolve` を経由しない
`Policy` から作った `Command` では `sudo -n` が前置されない。

`ssh.Executor.Execute(ctx, target, cmd)` は `Command.Sudo()` の値のみを根拠に `sudo -n` の前置と
`SudoAuto` 時の再実行を判断し、`policy.Policy` そのものは受け取らない・参照しない。
これにより sudo 判断ロジックは `policy.Resolve`（capability ∩ administrator policy の交差判定）に
一元化され、Executor 層で判断が分岐・重複することがない。

この不変条件（`policy.SudoMode` を直接受け取る exported 関数・メソッドが `internal/ssh` に存在しない
こと）は `internal/ssh/boundary_test.go` が `go/ast` によるソース解析で機械的に assert する。

## 検討した代替案

- **sealed interface で Command を表現する**: `internal/ssh` 外からの実装を防げるが、フィールドへの
  直接アクセスを防ぐには結局 unexported フィールド + アクセサメソッドが必要になり、unexported
  struct + unexported constructor よりも複雑になる。Go の慣用的な「unexported フィールドを持つ
  exported 型」で同じ保証を単純に達成できるため不採用。
- **Command を外部公開の struct にし、バリデーションで守る**: `Program` / `Args` / `Sudo` を
  exported フィールドにして、生成時ではなく Executor 実行直前にバリデーションする案。この場合、
  バリデーションを経由しない `Command{Sudo: policy.SudoAlways}` のような struct literal が
  コンパイル時点で作れてしまい、「Command を外部パッケージから生成不可にする」という issue #6 の
  制約そのものに反する。不採用。
- **Executor が `policy.Policy` を受け取り、Executor 自身が capability ∩ administrator policy の
  交差判定を行う**: sudo 判断ロジックが `policy.Resolve` と Executor の2箇所に分散し、
  どちらが最終的な判断点かが曖昧になる。将来 operation が増えるたびに Executor 側にも capability
  判定ロジックの複製が必要になり、sudo_contract を「1点」に保てなくなる。不採用。

## 影響

- 新しい operation を追加するたびに、`internal/ssh` パッケージ内に exported ファクトリ関数を1つ追加する
  必要がある（`Command` の直接構築はできないため）。これは意図した設計上の摩擦であり、Command 生成が
  常に `policy.Policy` を経由することを型システムで強制する。
- `internal/ssh/boundary_test.go` は s6-command が空スライスで作成し、以降の SSH ステップ
  （`UptimeCommand` を追加する s7-executor 等）がファクトリを追記していく形で運用する。
  新しい exported 関数を `internal/ssh` に追加するたびに、この境界テストが `policy.SudoMode` を
  直接受け取っていないことを検証する。
- sudo 判断が `policy.Resolve` に一元化されているため、config.yaml のバリデーション（issue #3、
  capability 外の sudo mode 設定を起動時エラーにする）と実行時の sudo 適用が同一の判断点を通る
  ことが保証され、両者が食い違う余地がない。
