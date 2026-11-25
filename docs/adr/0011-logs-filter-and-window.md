# 0011. logs の絞り込み・期間の終わりと絶対時刻・時間ごとの集計

## ステータス

Accepted

## コンテキスト

`logs` は「今から `--since` 前から今まで」の最新 `--lines` 件しか返せなかった。障害調査では次が要る。

- 特定の文言を含むエントリだけを見る（`connection refused` など）
- 「11/20 9 時から 11/21 9 時まで」のように、絶対時刻で期間の両端を決める
- 長い期間について、一致したエントリが時間ごとに何件あったかだけを知る

検索語や正規表現は任意の文字列であり、空白やシェルのメタ文字を含む。Focal は呼び出し元の値を
argv に置く前に `internal/vo` で許可リストに通す（ADR 0002）が、検索語をその許可リストに収めることはできない。

AL2023（systemd 252）の e2e コンテナで、相対の `--until` がリモートで期待どおり効くことを確かめた
（`docker exec -u e2e` で journalctl を直接実行）。

```text
$ journalctl -u focal-e2e-logger.service --since=-1h --until=-1s ...     # 起動時の 6 エントリがすべて返る
$ journalctl -u focal-e2e-logger.service --since=-1h --until=-30m ...    # -- No entries --（exit 0）
$ journalctl -u focal-e2e-logger.service --since=-5400s --until=-1h ...  # -- No entries --（exit 0。秒の期間も受ける）
$ journalctl -u focal-e2e-logger.service --since=-10s --until=-1h ...    # --since= must be before --until=.（exit 1）
```

`--since=@<秒>` / `--until=@<秒>` が効くこと、`-n` と `--reverse` で範囲内の最新 N 件が新しい順に返ること、
short-iso の offset が `+0900` であることは、ADR 0009 の採取で確かめてある。

## 決定

### 引数

`logs` に次を足す。CLI のフラグと MCP（`inspect_logs`）の引数は同じ名前にする。複数語の名前は
MCP でも `grep-regex` / `count-by` とハイフンでつなぐ（エラーの `field` も同じ綴り）。

- `--since`: 従来の期間に加えて RFC 3339 の時刻を受ける。値に `T` を含めば時刻として
  `vo.ParseTimestamp` に、含まなければ期間として `vo.ParseDuration` に渡す（期間の文法に `T` はない）
- `--until`: 新設。`--since` と同じ 2 つの形。期間は「今からその分だけ前」
- `--grep`: 固定文字列の部分一致（大文字小文字を区別）。`vo.LogPattern`
- `--grep-regex`: Go の RE2 正規表現。`vo.LogRegex`。`--grep` と同時に指定すると
  `conflicting_arguments`（field `grep-regex`）
- `--count-by`: `hour` か `day`。`vo.CountBy`

`kernel` には入れていない。入れる場合も同じ規則にする。`kernel` の `--since` は従来どおり期間だけを受ける。

### 値オブジェクト

- `vo.Timestamp`: `YYYY-MM-DDTHH:MM:SS` に `Z` か `±hh:mm` が続く形だけを受ける。小数秒・offset なし・
  小文字の `t` / `z`・コロンなしの offset は拒否する。1970-01-01T00:00:00Z 以前も拒否する。
  argv に置くのは `Epoch()`（正の 10 進数字）だけで、ゼロ値の `Epoch()` は空文字列になり argv の token に
  なれない。Focal が自分で計算した時刻（現在時刻から期間を引いたもの）は `vo.TimestampFromUnix` で作る
- `vo.LogPattern` / `vo.LogRegex`: 1〜256 バイト、制御文字（`\x00`-`\x1f`、`\x7f`）を拒否する。
  正規表現のコンパイルエラーは種類（`missing closing )` など）だけを伝える。regexp のエラー文は式そのものを
  引用するため、拒否した値をメッセージに写さないという `internal/vo` の規則に従う
- `vo.CountBy`: `hour` / `day`
- `vo.DurationOfSeconds`: Focal が計算した期間（下の「until だけ」の since）を、割り切れる最大の単位で書く。
  `ParseDuration` は数字 6 桁までなので、秒で書くと 11.5 日あたりを超えた期間が書けない
  （`12d` + `30m` は 1038600 秒）。分で割り切れれば 30d まで 6 桁に収まる（17310m）。
  どの単位でも 6 桁に収まらない期間（`999999s` + `30m` など、秒でしか割り切れない 11.5 日超）は
  `invalid_duration` で拒否し、丸めない

検索語・正規表現・集計単位を argv に入れる経路は作らない。journalctl の `--grep` も使わない。
`internal/ssh/boundary_test.go` の `TestNoFactoryTakesAFilterValue` が、`vo.LogPattern` / `vo.LogRegex` /
`vo.CountBy` を引数に取る関数が `internal/ssh` に無いことを検査する。

### remote argv

窓の形ごとに factory を分ける。`TestExportedFactoryParametersAreTypedValues` は factory の引数を
`policy.Policy` か `vo.` の型に限るので、`*vo.Timestamp` のような省略可能な引数は使えない。

| 窓の形 | factory | argv（`-u UNIT` の後） |
|---|---|---|
| since が期間、until なし | `LogsCommand`（従来のまま） | `--since=-D -n N` |
| since・until とも期間 | `LogsRelativeWindowCommand` | `--since=-D --until=-D -n N` |
| どちらかが時刻、until なし | `LogsSinceEpochCommand` | `--since=@S -n N` |
| どちらかが時刻、until あり | `LogsEpochWindowCommand` | `--since=@S --until=@S -n N` |

どれも後ろに `--reverse --no-pager --output=short-iso` が付く。期間と時刻が混ざる形は無い。
until を省略したら `--until` を渡さない（現在時刻で補わない）。

`-n` は必ず渡す。`--grep` / `--grep-regex` / `--count-by` のどれかを使う要求は、走査上限
`logsScanLimit`（100000、`internal/operation` の定数）を渡す。使わない要求は解決済みの `--lines` を渡す。

### 時計

- 両端とも期間（または since だけの期間）なら、リモートの時計を使う。argv も相対のまま
- どちらかが時刻なら、両端とも Focal の時計で時刻に直す。期間の側は Focal の現在時刻から引く
- 現在時刻は `operation.Registry` が `Build` のたびに 1 回読む（`Registry.clock`）。システム時計を読むのは
  そこだけで、テストはパッケージ内で `Registry.now` を差し替える。`focal serve` のように長く生きる
  Registry でも、要求ごとの時刻で判断する
- `Logs` は時計を持たない。解決済みの窓だけを値オブジェクトで持つ

### 検査

- until が since より前: `until_before_since`（field `until`）。等しいのは許す（journalctl は 0 件・exit 0 を返す）
- since が Focal の現在時刻より後: `since_in_future`（field `since`）。until の未来は許す
- `max_since`: 期間なら従来どおり秒数で比べる。時刻なら「since < 現在時刻 − max_since」を拒否する（許容幅なし）。
  `max_since` が設定されていなければ、`ParseDuration` の上限と同じ 30d を上限にする。エラーは従来の
  `since_above_max`（field `since`、allowed `at most <max_since>`）

### until だけを指定したとき

since は until から logs の既定の期間（30m）を引いた点にする。until が期間なら since も期間
（`--until 2h` → `--since=-150m --until=-2h`）、until が時刻なら since も時刻。

この since は `max_since` まで縮めず、指定された since と同じく `since_above_max` で拒否する。
since を省略した従来の要求は既定値を上限まで縮めるが、そちらは窓の終わりが「今」で、縮めても
「直近の範囲」という問いは変わらない。until を指定した要求で縮めると、呼び出し元が決めた終わりに対して
窓だけが短くなり、別の問いに答えてしまう。エラーのメッセージには、since が until から導いた値であることを書く。

### 絞り込みと集計（Focal 側）

- 単位はエントリ。区切りの規則は ADR 0009 と同じで、`journalSplit`（`internal/operation/journal_order.go`）を
  並べ替えと絞り込み・集計の両方が使う
- 一致の対象はエントリ全文（先頭行のタイムスタンプ・ホスト・識別子の接頭部と、継続行を改行でつないだもの）
- タイムスタンプで始まらない行（`-- No entries --` など、最初のエントリより前の行）は、エントリとして数えず、
  一致の対象にもしない。`-- Boot ... --` のようにエントリの間に出る行は、ADR 0009 と同じく直前のエントリの
  継続行として扱われるので、そのエントリの全文に含まれる
- Truncated のときは、途中で切れている可能性のある最古のエントリを 1 つ捨ててから数える（ADR 0009 と同じ）
- stdout:
  - `--count-by`: 空（本文は返さない）
  - `--grep` / `--grep-regex`: 一致したエントリのうち新しい側の `--lines` 件を古い順。各行は改行で終わる
  - どちらも使わず `--until` や時刻だけを使う要求: 従来の要求と同じ（`journalOldestFirst`。
    `-- No entries --` のような行もそのまま残る）
- 集計: 走査できた一致エントリをすべて数える（`--lines` には縛られない）。バケットは先頭行のタイムスタンプを
  リモートの offset のまま時・日で切ったもの。表記は時 `2026-11-25T13+0900`、日 `2026-11-25+0900`。
  offset はコロンを取り除いた形にそろえる（`+0900` と `+09:00` は同じバケットになる）。並びは古い順
  （古い順に並べたエントリで初めて現れた順）。エントリのないバケットは出さない

### 出力

- `result.Envelope` と `result.Part` に `Items []map[string]string`（`json:"items,omitempty"`）を足す。
  ほかの operation は使わないので、既存の JSON と golden は変わらない
- `--count-by` の結果は `items` に `{"bucket": "...", "count": "12"}` を古い順に並べる。値はすべて文字列
- 新しい引数（RFC 3339 の `--since` を含む）を 1 つでも使った要求は、成功・失敗にかかわらず `data` を返す:
  - `complete`: `"true"` / `"false"`
  - `scanned_entries`: 走査したエントリ数（Truncated で捨てた 1 件は数えない）
  - `matched_entries`: 一致したエントリ数（絞り込みなしなら `scanned_entries` と同じ）
  - `covered_from` / `covered_to`: 走査できたエントリの最古・最新の先頭行のタイムスタンプ（リモートの表記のまま）。
    エントリがなければキーを出さない
- `complete` が `"false"` になるのは、出力が Truncated / journalctl が返したエントリ数が渡した `-n` に達した /
  timeout / ssh の失敗 / journalctl の非 0 終了、のどれか。`-n` に達したときは、範囲内にそれより古い
  エントリが残っている可能性がある。絞り込みを使わず `--until` や時刻だけを使う要求では `-n` は `--lines` なので、
  `--lines` 件ちょうど返ったときも `"false"` になる
- `truncated`（`max_output` による切断）の意味は変えない
- 新しい引数をどれも使わない要求の argv と出力は ADR 0009 のまま。`data` も `items` も付けない

## 検討した代替案

- **journalctl の `--grep` を使う**: 走査をリモートで済ませられるが、検索語を argv に置くことになり、
  `argSpec` を広げるか検索語に別の許可リストを設けるかが要る。どちらも ADR 0002 の境界を広げる。
  また journalctl の `--grep` は PCRE2 で、systemd のビルドによっては使えない。不採用
- **until を省略したら現在時刻で補う**: 両端とも期間の要求でもリモートと Focal の時計を混ぜることになる。
  省略時は渡さない
- **until だけのときの since を `max_since` まで縮める**: 上の「until だけを指定したとき」のとおり不採用
- **factory を 1 つにして until を省略可能にする**: `*vo.Timestamp` のようなポインタや任意の型を引数に取ると
  ガードテストを弱めることになる。不採用

## 影響

- argv golden（`internal/ssh/testdata/argv_golden.txt`・`internal/ssm/testdata/commands_golden.txt`）に
  新しい 3 つの factory の行が加わる。既存の行は変わらない
- `--grep` / `--count-by` の要求は `-n 100000` で走査するため、出力が `max_output`（既定 10MiB）に
  当たりやすい。そのときは `complete` が `"false"` になり、`covered_from` が走査できた最古の時刻を示す
- SSM transport は stdout を 24,000 文字で打ち切るので、走査する要求はほぼ truncated になり、
  `covered_from` がごく最近になる
- 相対の指定はリモートの時計、時刻を含む指定は Focal の時計を基準にする。両者がずれていると、
  時刻での指定はその分ずれた範囲を読む
- 既存のテストのうち、`TestLogsHoldsOnlyTypedParameters`（`Logs` のフィールド一覧）、CLI の
  `TestOperationUsage`（logs の使い方の文字列）、MCP の `TestSchemasExposeOnlyTypedParameters`
  （`inspect_logs` の引数一覧）は、足した引数とフィールドの分だけ期待値を増やした
