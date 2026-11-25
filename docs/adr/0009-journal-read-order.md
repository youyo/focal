# 0009. journal は新しい側から取り、Focal 側でエントリ単位に古い順へ戻す

## ステータス

Accepted

## コンテキスト

`logs` と `kernel` は `journalctl ... --since=-<期間> -n <N> --no-pager --output=short-iso` を実行していた。
利用者が `--lines N` に期待するのは「期間内の最新 N 件」である。

systemd 252（Amazon Linux 2023）では、`--since` と `-n` を `--reverse` なしで併用すると、
期間の**古い方から** N 件が返る。e2e の AL2023 コンテナ（`test/e2e/systemd`）で確かめた。
`focal-e2e-logger.service` の journal は古い順に、systemd の "Starting…"、marker 1〜3、
改行を含む 1 エントリ、systemd の "Started…" である。

```text
$ journalctl -u focal-e2e-logger.service --since=@1795581901 --until=@1795581902 -n 2 --no-pager --output=short-iso
2026-11-25T13:45:01+0900 f9a83d872661 systemd[1]: Starting focal-e2e-logger.service - ...
2026-11-25T13:45:01+0900 f9a83d872661 focal-e2e-logger[29]: focal-e2e-logger: marker line 1 of 3
```

`focal <host> logs focal-e2e-logger --lines 2` も同じ 2 行を返していた。一方、`-n 2 --reverse` は
期間内の最新 2 件（"Started…" と改行入りのエントリ）を新しい順に返す。

short-iso では、改行を含むエントリの 2 行目以降は、本文の開始桁まで空白で字下げされた継続行になる。
タイムスタンプの offset は `+0900`（コロンなし）である。

## 決定

- `LogsCommand` と `KernelLogsCommand` の argv に `--reverse`（lit）を足す。時計の基準は
  これまでどおり `--since=-<期間>`（リモート側の時計）で、ほかの引数は変えない。
- 返ってきた新しい順の stdout を、Focal 側で古い順へ戻す（`internal/operation/journal_order.go`）。
  `logs` と `kernel` は同じ関数を使い、両者が通る `journalEnvelope` で 1 回だけ適用する。
  - 並べ替えの単位は行ではなくエントリである。タイムスタンプ（`YYYY-MM-DDTHH:MM:SS` に `+0900` か
    `+09:00` 形式の offset と空白が続く）で始まる行をエントリの先頭行とし、それ以外の行はすべて
    直前の先頭行の継続行とする。字下げの幅や形には依存しない。
  - 最初の先頭行より前の行はどのエントリにも属さないので、先頭に残す。
  - 先頭行が 1 つも無い出力（`-- No entries --` やエラー文言）は、そのまま返す。
- 出力が Truncated（`max_output` で切られた）のときは、末尾のエントリを 1 つ捨ててから並べ替える。
  逆順なので末尾は最も古いエントリで、切断が掛かりうるのはそのエントリだけである。継続行の途中で
  切れていても先頭行の途中で切れていても見分けられないため、切れていない場合も含めて捨てる。
  エントリが 1 つしかなければ、stdout は空になる。Status は従来どおり truncated である。
- Envelope の型・フィールドと stderr は変えない。stdout の並びが「期間内の最新 N 件を古い順」になる。

## 検討した代替案

- **`--reverse` を付けず `--since` を外す**: `-n` だけなら最新 N 件が古い順に返るが、期間の指定と
  上限（`max_since`）が効かなくなる。不採用。
- **行単位で逆順にする**: 改行を含むエントリの継続行が先頭行より前に来てしまう。不採用。

## 影響

- 出力の上限（`max_output`）は stdout と stderr で共有している。stderr 側で上限に達して Truncated に
  なった場合も、stdout の最古のエントリを 1 つ捨てる（そのエントリが切れていなくても捨てる）。
- SSM transport で SSM 自身が出力を打ち切ったとき、stdout 末尾の打ち切り印（`---Output truncated---`）は
  最古のエントリと一緒に stdout から消える。Envelope の `truncated` と status（truncated）は残る。
- `kernel` は unit テストと argv golden でのみ確認している。e2e のコンテナでは `journalctl -k` が
  `-- No entries --` しか返さないため、実機で中身のある出力を並べ替えた結果は未確認である。
- 期間が再起動をまたぐと、journalctl はエントリの間に `-- Boot <id> --` のような行を出すことがある
  （e2e のコンテナは 1 回しか起動しないので未確認）。この行は直前のエントリ（逆順では新しい側）の
  継続行として扱われるため、並べ替え後は boot の境界から 1 エントリずれた位置に出る。行そのものは
  失われない。
- argv golden（`internal/ssh/testdata/argv_golden.txt`・`internal/ssm/testdata/commands_golden.txt`）の
  `logs` / `kernel` の行が変わる。
