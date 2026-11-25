# Focal

> Give coding agents visibility, not shell access.

**Focal never accepts arbitrary remote commands.**

Focal exposes a fixed set of structured inspection operations and executes them through your existing OpenSSH configuration. Focal does not replace SSH configuration — it constrains what may be executed through SSH.

Focal は Coding Agent 向けの制限付き SSH 検査ツールです。Agent が制御できるのは「どの host を見るか」「どの built-in operation を使うか」「その operation が明示的に公開した型付き parameter」だけで、実際の Linux コマンドは一切制御できません。出力は Coding Agent が消費しやすい構造化 JSON を既定とします。

## 命名

Focal は「Lens」の世界観から生まれました — Agent がサーバーを安全に見るための光学系です。命名は
[focal point](https://en.wikipedia.org/wiki/Focus_(optics))（焦点）から取っています。**Agent can look,
but not touch.**

## Built-in operations

すべての operation は Focal が固定した argv だけを実行します。「可変入力」列にある値だけが Agent
（CLI/MCP の呼び出し元）から渡り、それ以外は Focal 自身のコードに書かれた文字列リテラルです。

| Operation | 実行コマンド | sudo 既定 | 可変入力 |
|---|---|---|---|
| `system` | `uname -a` / `hostname` / `uptime` / `date --iso-8601=seconds` / `cat /etc/os-release` | never | なし |
| `cpu` | `lscpu` / `cat /proc/loadavg` | never | なし |
| `memory` | `free -b` / `cat /proc/meminfo` | never | なし |
| `storage` | `df -PT` / `lsblk -o NAME,TYPE,SIZE,FSTYPE,MOUNTPOINTS` / `findmnt` | never | なし |
| `network` | `ip -details addr show` / `ip route show` / `ss -lntup` / `cat /etc/resolv.conf` | never | なし |
| `processes` | `ps -eo pid,ppid,user,state,%cpu,%mem,etime,nlwp,comm,args`（フィルタは remote に渡さず Focal 側でローカルに適用） | never | `--name`, `--pid` |
| `service` | `systemctl show UNIT --no-pager --property=Id,Description,LoadState,ActiveState,SubState,MainPID,ExecMainStatus,Result,TasksCurrent,TasksMax,NRestarts,MemoryCurrent` | never | `SERVICE`（例: `nginx` → `nginx.service` に正規化） |
| `package` | `rpm -q NAME`。終了コードが 0 以外なら続けて `dpkg-query -s NAME`（[ADR 0010](docs/adr/0010-package-operation.md)） | never | `NAME`（例: `procps`, `libstdc++6`） |
| `logs` | `journalctl -u UNIT --since=-DURATION -n LINES --reverse --no-pager --output=short-iso`（`--until` や絶対時刻を使うときの形は[下記](#logs-の絞り込み期間集計)） | auto（`never` へ変更可） | `SERVICE`, `--since`（既定 `30m`。期間か RFC 3339）, `--until`, `--lines`（範囲内の最新 N 行（古い順に並べて返す）。既定 `200`）, `--grep`, `--grep-regex`, `--count-by` |
| `kernel` | `journalctl -k --since=-DURATION -n LINES --reverse --no-pager --output=short-iso` | auto（`never` へ変更可）。**既定 disabled** | `--since`（既定 `1h`）, `--lines`（範囲内の最新 N 行（古い順に並べて返す）。既定 `200`） |
| `inspect` | remote command なし。`system` / `cpu` / `memory` / `storage` / `network` / `processes` を最大3並列で実行し結果を合成する composite operation | never | なし |

sudo モードは `never` / `auto` / `always` の3値ですが、`always`（起動から常時 `sudo -n` を付ける）を
選べる operation は現状ありません。`auto` は無権限で一度実行し、権限不足を示す終了コード
（1 / 13 / 77 / 126）が返ったときだけ `sudo -n` を付けて一度だけ再実行します。`inspect` を含め
すべての operation の sudo capability は `internal/policy` が operation ごとに固定しており、config
ファイルはその範囲を狭めることしかできません。

### logs の絞り込み・期間・集計

`logs` は、期間の終わり（`--until`）、絶対時刻での期間指定、エントリの絞り込み（`--grep` /
`--grep-regex`）、時間ごとの件数の集計（`--count-by`）を受け付けます
（[ADR 0011](docs/adr/0011-logs-filter-and-window.md)）。MCP の `inspect_logs` でも同じ名前の引数
（`until` / `grep` / `grep-regex` / `count-by`）です。`kernel` はこれらを受け付けません。

| 引数 | 受け付ける値 |
|---|---|
| `--since` | 期間（`30m`・`24h`・`7d` など。今からその分だけ前）か、RFC 3339 の時刻（`2026-11-20T09:00:00+09:00`、`2026-11-20T00:00:00Z`）。時刻は offset 必須で、小数秒は受け付けない |
| `--until` | `--since` と同じ形。期間は「今からその分だけ前」。省略すると終わりを指定しない（`--until` を渡さない） |
| `--grep TEXT` | この文字列を含むエントリだけを返す。固定文字列の部分一致で、大文字小文字を区別する。1〜256 バイト、制御文字は不可 |
| `--grep-regex RE` | この正規表現（Go の RE2。後方参照・先読みは不可）に一致するエントリだけを返す。`--grep` とは同時に指定できない |
| `--count-by hour\|day` | 一致したエントリを時（hour）か日（day）ごとに数え、`items` に返す。stdout は空になる |

**実行されるコマンド。** 期間の両端がどちらも期間（相対）なら、リモートの時計を基準にそのまま渡します。
どちらかが RFC 3339 の時刻なら、両端とも Focal を実行している側の時計で Unix 秒に直して渡します
（期間の側は Focal の現在時刻から引きます）。

```
journalctl -u UNIT --since=-DURATION [--until=-DURATION] -n N --reverse --no-pager --output=short-iso
journalctl -u UNIT --since=@SECONDS  [--until=@SECONDS]  -n N --reverse --no-pager --output=short-iso
```

- `--until` だけを指定したときの since は、until から既定の `30m` を引いた点です（`--until 2h` なら
  `--since=-150m --until=-2h`、until が時刻なら since も時刻）。
- `--grep` / `--grep-regex` / `--count-by` のどれかを使うと、`-n` は `--lines` ではなく走査上限の
  `100000` になります。検索語・正規表現はリモートに渡しません（journalctl の `--grep` も使いません）。
  読み戻したエントリに Focal 側で適用します。
- 次の要求は実行前に拒否します: until が since より前、since が未来（until の未来は可）、since が
  `max_since` より前（時刻のときは「Focal の現在時刻 − `max_since`」より前）。

**絞り込みの単位はエントリです。** タイムスタンプで始まる先頭行と、それに続く継続行（改行を含む
メッセージの 2 行目以降）で 1 エントリです。`--grep` / `--grep-regex` はエントリ全文、つまり先頭行の
タイムスタンプ・ホスト名・識別子（`nginx[123]:` など）と継続行を含めた全体（行は改行でつなぐ）に
対して一致を見ます。`-- No entries --` のようにタイムスタンプで始まらない行は、エントリとして数えず、
一致の対象にもしません。stdout には一致したエントリのうち新しい側の `--lines` 件を、古い順に返します。

**集計のバケット**は、リモートが出力した offset のまま、先頭行のタイムスタンプを時か日で切ったものです。
offset はコロンなしの形にそろえます（`+09:00` で出力されたエントリも `+0900` のバケットに入ります）。

| `--count-by` | バケットの表記 |
|---|---|
| `hour` | `2026-11-25T13+0900` |
| `day` | `2026-11-25+0900` |

集計は走査できた一致エントリをすべて数え（`--lines` には縛られません）、エントリのないバケットは出しません。

**出力。** 上の引数（RFC 3339 の `--since` を含む）を 1 つでも使った要求は、`data` に次のキーを持ちます。
値はすべて文字列です。どれも使わない要求の出力は従来どおりで、`data` も `items` も付きません。

| キー | 意味 |
|---|---|
| `complete` | `"true"` / `"false"`。次のどれかなら `"false"`: 出力が `max_output` で切れた（truncated）、走査したエントリ数が `-n` に達した（範囲内にまだ古いエントリが残っている可能性がある）、timeout、ssh の失敗、journalctl の非 0 終了 |
| `scanned_entries` | 走査したエントリ数 |
| `matched_entries` | 一致したエントリ数（絞り込みなしなら `scanned_entries` と同じ） |
| `covered_from` / `covered_to` | 走査できたエントリのうち最古・最新の先頭行のタイムスタンプ（リモートの表記のまま）。エントリがなければキー自体を出さない |

`--count-by` の結果は `items` に、古いバケットから順に並びます。

```json
{"operation":"logs","host":"prod-web","status":"ok","exit_code":0,"duration_ms":412,
 "data":{"complete":"true","covered_from":"2026-11-25T12:30:02+0900","covered_to":"2026-11-25T13:59:58+0900","matched_entries":"15","scanned_entries":"4210"},
 "items":[{"bucket":"2026-11-25T12+0900","count":"3"},{"bucket":"2026-11-25T13+0900","count":"12"}]}
```

出力が切れたとき（truncated）は、途中で切れている可能性のある最古のエントリを 1 つ捨ててから数えます。
`covered_from` を見れば、実際にどこまで遡って走査できたかがわかります。

**十数日の調査には `max_since` を上げます。** 既定の `max_since` は `24h` で、これより前を since に
指定すると拒否されます。設定できる上限は `30d` です。

```yaml
operations:
  logs:
    max_since: 14d
```

```sh
focal prod-web logs nginx --since 2026-11-10T00:00:00+09:00 --until 2026-11-20T00:00:00+09:00 --grep "upstream timed out" --count-by day
```

**SSM transport では走査できる範囲が短くなります。** SSM は stdout を 24,000 文字で打ち切るため、
`--grep` や `--count-by` で走査する要求はほぼ truncated になり、`covered_from` がごく最近の時刻に
なります（`complete` は `"false"`）。新しい側から読むので、走査できるのは期間の終わり側の一部です。

## Requirements

Focal は接続先への SSH 実行に、利用者の環境にインストール済みの OpenSSH クライアントをそのまま使います。**OpenSSH クライアント 10.1 以降を推奨します**(CVE-2023-51385 / CVE-2025-61984 は `ssh_config` の `%h` / `%u` 展開経由の間接的な injection 経路であり、最新の OpenSSH クライアントを使うことが多層防御の一段になります)。

## 設定ファイル

Focal は `$XDG_CONFIG_HOME/focal/config.yaml`（`XDG_CONFIG_HOME` 未設定なら
`~/.config/focal/config.yaml`）を起動時に一度読みます。`XDG_CONFIG_HOME` が相対パスのときは XDG の仕様どおり無効として
無視し、未設定と同じ扱いにします。ファイルが存在しない場合は、読み取り専用
operation がすべて有効・`kernel` だけ無効・sudo はどこにも掛からないという安全側デフォルトで動きます。
未知のキーや範囲外の値はすべて起動時エラーであり、意図しない弱い設定へ黙って落ちることはありません。

```yaml
execution:
  timeout: 30s                          # 1s〜10m、既定 30s。1コマンドあたりの実行時間の上限
  max_output: 10MiB                     # 1KiB〜1GiB、既定 10MiB。1コマンドの stdout+stderr 合計の上限
  port: 2222                            # 1〜65535。省略時は ssh(1) / ~/.ssh/config に委任
  identity_file: "~/.ssh/id_ed25519"    # 省略時は ssh(1) / ~/.ssh/config に委任

operations:
  kernel:
    enabled: true      # 既定 false。有効化して初めて CLI/MCP から呼べるようになる
  package:
    enabled: false     # 既定 true（読み取り専用）。false にすると CLI からも MCP からも呼べなくなる
  logs:
    sudo: auto          # never（既定）| auto。journalctl が読み取り権限不足で失敗したときだけ sudo -n で1回だけ再実行する
    max_lines: 500       # 既定上限 1000。--lines / lines が指定されないときの既定値(200)はこの上限まで縮められる
    max_since: 6h         # 既定上限 24h。--since / since が指定されないときの既定値(logs は 30m、kernel は 1h)はこの上限まで縮められる
```

`execution.max_output` は `1KiB` / `1MiB` / `1GiB`（2進接頭辞）または裸のバイト数のみを受け付けます
（`1KB` のような10進単位は意図的にサポートしていません）。`operations.<name>.max_lines` /
`operations.<name>.max_since` を持てるのは行ストリームを読む `logs` と `kernel` だけで、他の operation
に書くと起動時エラーになります。`operations.<name>.sudo` に選べる値は operation ごとの capability
（上の一覧表の「sudo 既定」列）の範囲内に限られ、範囲外を指定した場合も起動時エラーです。

## CLI

```
focal [flags] HOST OPERATION [args]
```

`ssh` と同じく接続先を最初に書きます。フラグは HOST より前にしか置けません（HOST より後ろの `--since`
などは operation 自身の引数として解釈され、グローバルフラグとしては読まれません）。`-o` に相当する
オプションはなく、`focal HOST exec` / `run` / `shell` / `command` のような任意コマンド実行の入口も
存在しません。

```sh
focal prod-web inspect
focal prod-web service nginx
focal prod-web package openssh-server
focal prod-web logs nginx --since 30m --lines 500
focal ubuntu@10.0.1.20 system
focal -i ~/.ssh/customer-a.pem -p 2222 -l ec2-user web01 kernel --since 1h
```

グローバルフラグ:

| フラグ | 意味 |
|---|---|
| `-i, --identity` | 認証に使う秘密鍵ファイル |
| `-p, --port` | SSH ポート |
| `-l, --user` | ログインユーザー（HOST がユーザーを含まないとき）。HOST に `user@` を書いた上で `-l` も指定するとエラーになる |
| `--json` | 結果を1行の JSON で出力（既定の挙動を明示するだけのフラグ） |
| `--pretty` | 結果をインデント付き JSON で出力（`--json` と排他） |
| `--timeout` | 1コマンドあたりの実行時間の上限、例 `30s` |
| `--config` | 既定の場所の代わりに読む設定ファイル |

接続設定の優先順位は **CLI のフラグ → 設定ファイル → `~/.ssh/config` / ssh(1) の既定値** です。

`-i` / `--identity`（設定ファイルの `identity_file` も同じ）は、ssh-agent や `~/.ssh/config` が持つ他の鍵の提示を止めません。
指定した鍵だけに限定したい場合は `~/.ssh/config` で `IdentitiesOnly yes` を設定してください。

## SSM transport

SSH に到達できない環境（鍵配布が難しい、22番ポートを開けたくない、既に AWS Systems Manager で
EC2 を管理している等）向けに、`ssh(1)` の代わりに AWS SSM Run Command（`AWS-RunShellScript`）で
built-in operation を実行する transport です（[ADR 0007](docs/adr/0007-ssm-transport.md)）。
HOST の意味以外、operation の意味・引数は SSH transport とまったく同じです。

**設定ファイルで明示的に有効化する必要があります。**既定は `execution.transports: [ssh]` で、
`ssm` を含めない限り CLI・MCP どちらからも呼び出しはエラーになります。

```yaml
execution:
  transports: [ssh, ssm]   # 既定は [ssh] のみ。ssm を使うにはここに追加する
```

EC2 側の要件:

- SSM Agent が動作していること（Amazon Linux 2/2023 など多くの公式 AMI にはプリインストール済み）
- `AmazonSSMManagedInstanceCore` を含む instance profile がアタッチされていること
- SSM の Custom Document は不要です。Focal は AWS 標準の `AWS-RunShellScript` のみを使います

Focal を動かす側（IAM）の要件: AWS SDK 標準の認証チェーン（環境変数 / `~/.aws/credentials` /
EC2 instance profile）と、リージョンの指定が必要です。`AWS_REGION`（または `AWS_DEFAULT_REGION`）が
未設定の場合、Focal が EC2 インスタンス上で動いていれば instance metadata service（IMDS）から
リージョンを取得しますが、それでも決められない場合は `SendCommand` を試みる前に明確なエラーを
返します。**EC2 インスタンス上で動いていない環境**（ラップトップ等）で `AWS_REGION` /
`AWS_DEFAULT_REGION` を設定せずに `--transport ssm` を使うと、IMDS への到達を試みて失敗するまで
数秒待たされます（未設定・ネットワーク到達不可な環境での実測で約4.5秒）。IMDS 自体を使わないと
分かっている場合は `AWS_EC2_METADATA_DISABLED=true` を設定するとこの待ちを省けます。
Focal 自身にはプロファイルやリージョンを指定するフラグ・設定はありません。
必要な IAM 権限は最小権限のサンプルを
[`docs/iam/focal-ssm-policy.json`](docs/iam/focal-ssm-policy.json) に置いています
（`ssm:SendCommand` はタグ条件付きの instance ARN と `AWS-RunShellScript` document の2ステートメント、
`ssm:GetCommandInvocation` / `ssm:CancelCommand` は `Resource: "*"`）。

CLI:

```sh
focal --transport ssm i-0123456789abcdef0 system
```

HOST は EC2 インスタンス ID（`i-` プレフィックス、`user@` 相当の指定は不可）を書きます。
`--identity` / `--port` / `--user` は SSM に対応する概念がなく、`--transport ssm` と同時に
指定するとエラーになります。

MCP:

```json
// inspect_system
{"host": "i-0123456789abcdef0", "transport": "ssm"}
```

`identity` / `user` は `transport: "ssm"` と同時に指定するとエラーになります。

**AWS-RunShellScript は常に root でコマンドを実行します。**`sudo` 設定で `always` を選んだ
operation だけ `sudo -n` を前置し（SSH と同じ最初の判断）、`auto` の「非零終了コードでの1回だけの
再試行」は行いません（root で動くため意味を持たないからです）。`sudo` を `never` にした operation も、SSM transport では root で実行されます。

出力には AWS 側の固定上限があります（stdout 24000文字 / stderr 8000文字）。これを超えた出力や
`execution.max_output` を超えた出力は、SSH transport と同じく `truncated: true` として報告されます
（S3 への完全な出力保存は ROADMAP 参照）。

## MCP over stdio（`focal mcp`）

`focal mcp` は `focal serve` と同じ MCP tool を、stdin / stdout で提供します。MCP クライアントが
focal を子プロセスとして起動し、改行区切りの JSON-RPC でやり取りする形です。手元の Coding Agent
1 つから使うだけなら、待ち受け口を開ける `focal serve` ではなくこちらを使ってください
（[ADR 0008](docs/adr/0008-stdio-transport-and-serve-auth-default.md)）。

```sh
focal mcp [--identity alias=path]... [--user USER] [--config PATH]
```

- `--identity` / `--user` / `--config` の意味は `focal serve` と同じです。ツールの一覧・引数・結果・
  identity alias の解決順序も `focal serve` と同じです（下の「MCP（`focal serve`）」を参照）。
- 待ち受け口を持たないので、`--listen` などの待ち受けのフラグはありません。upstream token も使いません。
  `FOCAL_UPSTREAM_TOKEN` が設定されていると、使わないことを stderr に知らせます
  （値は表示しません）。この変数はどのコマンドでも起動時に focal 自身の環境から取り除かれ、
  起動する ssh には引き継がれません。
- stdout には MCP の JSON-RPC メッセージだけを書きます。ログや警告は stderr に出ます。
  起動時に拒否した場合（設定ファイルや identity の誤り、未知のフラグなど）は、ほかのコマンドと同じ
  エラー JSON を stderr に書き、stdout には何も書きません。
- 同時に実行する tool call は 8 つまでです（`focal serve` と同じ上限）。それを超えた呼び出しは待ちます。
- クライアントが stdin を閉じるか、SIGINT / SIGTERM を受けると、実行中の処理を止めて終了コード 0 で
  終了します（5 秒未満）。stdin に JSON として読めない行が届いた場合は、エラー JSON を stderr に書いて
  終了コード 1 で終了します。
- プロトコル版は go-sdk に任せています。`initialize` を送る流儀と、リクエストごとに `_meta` を付ける
  `2026-07-28` の流儀のどちらでも話せます。

Claude Code に登録する例（`claude mcp add` か、プロジェクトの `.mcp.json`）:

```sh
claude mcp add focal -- focal mcp --identity default=~/.ssh/id_ed25519
```

```json
{
  "mcpServers": {
    "focal": {
      "command": "focal",
      "args": ["mcp", "--identity", "default=~/.ssh/id_ed25519"]
    }
  }
}
```

Codex に登録する例（`~/.codex/config.toml`）:

```toml
[mcp_servers.focal]
command = "focal"
args = ["mcp", "--identity", "default=~/.ssh/id_ed25519"]
```

`--identity` のパスの先頭の `~/` は focal 自身がホームディレクトリに展開するので、シェルを経由しない
起動でもそのまま書けます。

`mcp` という名前のホストは、`serve` と同じくサブコマンドの陰に隠れます。`focal mcp system` は
`mcp` サブコマンドとして解釈され、使い方の誤りとして拒否されます。`user@mcp` と書くか、
`~/.ssh/config` で別名を付けてください。

## MCP（`focal serve`）

`focal serve` は CLI と同じ operation を、stateless Streamable HTTP（MCP spec `2026-07-28`）の
MCP tool として公開します。**focal 自身はユーザーを認証しません**（誰が呼んでいるかを判定する機構は
持ちません）。

**TCP で待ち受けるには、アドレスによらず upstream token（`FOCAL_UPSTREAM_TOKEN`）が必要です。**
既定のアドレスは `127.0.0.1:8080` ですが、token が設定されていなければ loopback でも起動しません
（`unauthenticated_listen_address` で拒否し、exit 2）。loopback が防ぐのは別ホストからの到達だけで、
同じマシン上の別ユーザー・別プロセスは `127.0.0.1:8080` に接続でき、運用者の SSH 鍵・AWS 認証情報で
検査を実行できてしまうからです。token なしで起動できるのは次の 2 つだけです。

- `--listen unix:/path` の Unix ソケット（モード 0600）
- ローカルの単一クライアントから使う場合の `focal mcp`（stdio。待ち受け口を開けません。上の節を参照）

token は focal が生成しません。`openssl rand -hex 32` などで運用者が生成し、secret 管理から環境変数で
渡してください。

```sh
export FOCAL_UPSTREAM_TOKEN="$(cat /run/secrets/focal-upstream-token)"   # 例: secret 管理から読む
focal serve --listen 127.0.0.1:8080 \
  --user ec2-user \
  --identity default=~/.ssh/id_ed25519 \
  --identity customer-a=~/.ssh/customer-a.pem
```

focal と呼び出し元の間の境界は次の 2 つの手段で引きます。どちらも単独で運用でき、併用もできます。

- **Unix domain socket**（`--listen unix:/run/focal/focal.sock`）— 境界をファイルシステムの
  パーミッションで引きます。socket は `0600` で作成されるため、reachable なのは focal を動かしている
  ユーザーだけで、別ホストからは原理的に到達できません。**`0600` が防ぐのは別ユーザーからのアクセスだけで、
  同一ユーザーの別プロセスからの接続は防げません。** UDS では token は不要です（設定されていれば、
  UDS でも従来どおり token を要求します）。
- **固定 shared secret**（環境変数 `FOCAL_UPSTREAM_TOKEN`）— 境界をリクエストに載る
  値で引きます。token が設定されているリクエストにのみ `Authorization: Bearer <token>` を要求し、
  一致しなければ `401`（body は固定文字列、token・設定値・スタック情報は含みません）を返します。token は
  32 バイト以上の可視 ASCII が必要です。コマンドライン引数はプロセス一覧から同じマシンの他ユーザーにも
  読めるため、token を渡すフラグはなく、環境変数だけで受け取ります（読み取った後は focal 自身の環境から
  取り除かれ、起動する ssh には引き継がれません）。TCP の待ち受けには必須です。token があれば
  loopback 以外のアドレスにも bind できますが、通信は暗号化されないため（token も応答も平文で
  流れます。起動時に stderr へ警告を出します）、loopback 以外で bind するなら前段に TLS 終端プロキシを
  置いてください。token が設定されているとき、focal は `Host` ヘッダーを検査しません（前段のプロキシが
  外部名の `Host` をそのまま転送しても、token が正しければ応答します）。

### 前段にプロキシを置くときの要件

`focal serve` の前に認証プロキシや TLS 終端プロキシを置くときは、待ち受けの形（loopback の TCP、
loopback 以外の TCP、Unix ソケットのどれでも）によらず、次の要件を満たしてください。focal は `Host`
ヘッダーを検査しません。届いたリクエストを誰が送ったのかを確かめるのは、前段のプロキシの責務です。

- 利用者の認証は、リクエストごとに明示的に付く資格情報（`Authorization: Bearer` など）か、オリジンに
  縛られた資格情報（`Domain` 属性の無い host-only の Cookie など）で行う。
- 接続元だけで決まる認証（IP アドレスの許可リスト、tailnet の身元、Kerberos / Negotiate、クライアント
  証明書、認証なし）を使う場合は、プロキシ自身が `Host` を自分の名前と照合するか、HTTPS だけで公開する。

接続元だけで利用者を判断するプロキシは、ブラウザが開いた別のサイトからのリクエストも、正規の利用者の
ものとして扱います。そのとき、プロキシが token を付けたまま focal へ転送してしまいます（DNS rebinding）。
`Host` の照合か HTTPS だけの公開は、ブラウザが別のサイトの名前で届けたリクエストを、プロキシの段階で
止めるための要件です。

[idproxy](https://github.com/youyo/idproxy)（v0.10.2 以降）はこの要件を満たします。

### token なしの loopback で動かしていた構成の移行

token なしで TCP に待ち受けるためのフラグは廃止しました（指定すると未知のフラグとして exit 2 で失敗します）。
token なしの TCP（`127.0.0.1:8080` など）で `focal serve` を動かしていた構成は、起動しなくなります。
次のどちらかに移してください。手元の Coding Agent 1 つから使うだけなら、`focal mcp` に切り替えるのが
いちばん簡単です。

**Unix ソケットに移す（systemd の例）**

```ini
# /etc/systemd/system/focal.service
[Service]
User=focal
RuntimeDirectory=focal          # /run/focal を focal ユーザー所有・0755 で作る
ExecStart=/usr/local/bin/focal serve --listen unix:/run/focal/focal.sock \
  --identity default=/home/focal/.ssh/id_ed25519
```

前段のプロキシは同じ UID で動かし、`unix:///run/focal/focal.sock` に接続させます（下の方式A）。

**token を secret 管理から渡す（systemd / docker compose の例）**

```ini
# /etc/systemd/system/focal.service
[Service]
User=focal
# 中身は FOCAL_UPSTREAM_TOKEN=<openssl rand -hex 32 の出力> の 1 行。所有者 root・モード 0600 にする
EnvironmentFile=/etc/focal/upstream-token.env
ExecStart=/usr/local/bin/focal serve --listen 127.0.0.1:8080 \
  --identity default=/home/focal/.ssh/id_ed25519
```

```yaml
services:
  focal:
    image: ghcr.io/youyo/focal:latest
    command: ["serve", "--listen", "127.0.0.1:8080", "--identity", "default=/keys/id_ed25519"]
    env_file:
      - ./focal-token.env   # FOCAL_UPSTREAM_TOKEN=... の 1 行。リポジトリにコミットしない
    # 127.0.0.1 はこのコンテナの loopback。前段のプロキシは network_mode で同じネットワーク
    # namespace に置く（下の方式B）
```

前段のプロキシには同じ token を持たせ、`Authorization: Bearer <token>` を付けて転送させます（下の方式B）。

- **host は allowlist にしません。** 制限すべきは「どこを見るか」ではなく「そこで何ができるか」であり、
  「突然渡されたサーバーを安全に調査させられる」ことが Focal の価値です。
- **identity は事前登録した alias 経由でのみ選べます。** Agent はツール呼び出しで
  `"identity": "customer-a"` のように alias 名を渡すだけで、鍵ファイルのパスを直接指定する経路は
  ありません。alias を指定しなかった呼び出しは `default` alias（登録があれば）に解決されます。
- **`~/.ssh/config` の解釈はここでも OpenSSH に委任します。** `focal serve` を起動したユーザーの
  `~/.ssh/config` / `known_hosts` / ssh-agent がそのまま使われ、`{"host": "prod-web"}` という呼び出しは
  `ssh prod-web -- ...` として実行されます。MCP 側で接続先を別途定義する必要はありません
  （[ADR 0004](docs/adr/0004-delegate-ssh-config-in-remote-mcp.md)）。
- 解決順序は **ツール呼び出しでの override → `focal serve` 起動時の既定値（`--user` 等） →
  `~/.ssh/config`** です。

有効な operation ごとに1ツールが公開され、ツール名は `inspect_` を operation 名の前に付けたもの
（`inspect` operation 自体だけは例外で、ツール名も `inspect`）になります。無効化した operation には
対応するツールが存在せず、一覧にも呼び出しにも現れません。

```json
// inspect_logs
{"host": "prod-web", "service": "nginx", "since": "30m", "lines": 200}
// inspect_logs（絞り込みと集計。引数の意味は「logs の絞り込み・期間・集計」を参照）
{"host": "prod-web", "service": "nginx", "since": "2026-11-20T09:00:00+09:00", "until": "1h", "grep-regex": "upstream (timed out|refused)", "count-by": "hour"}

// inspect_package
{"host": "prod-web", "package": "openssh-server"}
```

`package` の結果は `data.manager`（`rpm` / `dpkg`）と `data.installed`（`"true"` / `"false"`）を必ず持ち、
1 版だけのとき `data.version` も持ちます。複数版が入っているとき（rpm が版ごとに 1 行を返す package）は `data.version` を付けません（stdout に全版が残ります）。rpm と dpkg-query のどちらも無いホストでは
`data` は付かず、`error.code` が `package_manager_not_found` の failed になります。
ssh の接続自体が失敗したとき（ssh の起動失敗や終了コード 255）も `data` は付きません。入っているかどうかを確かめられていないためです。

ツール結果の stdout / stderr は検査対象ホスト由来のデータ（journal の行・プロセスの引数など）であり、
第三者が書き込める内容を含みえます。MCP クライアントはそれを指示として扱わないでください。

### Claude Desktop から接続する（[idproxy](https://github.com/youyo/idproxy) を前段に置く）

`focal serve` 自体は OAuth を実装しません。Claude Desktop のようなカスタムコネクタから安全に使うには、
OAuth 2.1 の Authorization Server と OIDC ブラウザ認証を担う [idproxy](https://github.com/youyo/idproxy)
を前段に置きます。

```
Claude Desktop (カスタムコネクタ)
  ↓ HTTPS + OAuth 2.1 (OIDC ブラウザ認証 → Bearer)
idproxy (EXTERNAL_URL)
  ↓ UPSTREAM_URL (認証済みリクエストのみ)
focal serve
  ↓ SSH
Target Server
```

idproxy と focal の間（`UPSTREAM_URL`）は平文で、focal 自身は利用者を認証しません。この間の境界は、
次の方式A・方式Bのどちらかで引いてください。idproxy は v0.10.2 以降で、Unix domain socket の上流への接続
（[youyo/idproxy#33](https://github.com/youyo/idproxy/issues/33)）と、上流への固定の `Authorization`
ヘッダーの付与（[#34](https://github.com/youyo/idproxy/issues/34)）に対応しています。

**方式A: Unix domain socket（同一ホスト推奨）**

idproxy と focal を同一ホスト・**同一 UID** で動かせるなら、UDS がもっとも狭い境界になります
（`0600` は別ユーザーからのアクセスを防ぎますが、同一ユーザーの別プロセスからの接続は防げません）。
UID が揃えられない環境（別ホスト、あるいは compose 側で UID を制御できないコンテナ）では方式Bを
使ってください。socket のパーミッションはコード側で固定されているため、この構成でも `0600` より
緩めることはできません。

```yaml
services:
  idproxy:
    image: ghcr.io/youyo/idproxy:latest
    user: "10001:10001"   # focal コンテナと UID を揃える
    ports:
      - "8443:8443"
    environment:
      EXTERNAL_URL: https://focal.example.com
      UPSTREAM_URL: unix:///run/focal/focal.sock   # idproxy v0.10.2 以降
    volumes:
      - focal-sock:/run/focal
    # OIDC プロバイダの設定等は idproxy 側のドキュメントを参照

  focal:
    image: ghcr.io/youyo/focal:latest
    user: "10001:10001"   # idproxy コンテナと同一 UID。異なると 0600 socket に idproxy が接続できない
    command: ["serve", "--listen", "unix:/run/focal/focal.sock", "--identity", "default=/keys/id_ed25519"]
    volumes:
      - ~/.ssh:/root/.ssh:ro
      - ./keys:/keys:ro
      - focal-sock:/run/focal

volumes:
  focal-sock:
```

**方式B: 固定 shared secret（コンテナ跨ぎ・別ホスト）**

UID を揃えられない、あるいは focal と idproxy が別ホストにいる場合は upstream token を使います。
token が設定されているとき、focal は `Host` ヘッダーを検査しません。idproxy が外部名の `Host`
（`focal.example.com` など）をそのまま転送しても、loopback の待ち受けで拒否されることはありません。
token はプレースホルダです。実値は `openssl rand -hex 32` 等で生成し、リポジトリにコミットしないでください。

```yaml
services:
  idproxy:
    image: ghcr.io/youyo/idproxy:latest
    ports:
      - "8443:8443"
    environment:
      EXTERNAL_URL: https://focal.example.com
      UPSTREAM_URL: http://127.0.0.1:8080   # focal と同じネットワーク namespace の loopback
      UPSTREAM_AUTH_TOKEN: "<FOCAL_UPSTREAM_TOKEN と同じ値>"   # idproxy 側が Authorization: Bearer として注入
    # OIDC プロバイダの設定等は idproxy 側のドキュメントを参照

  focal:
    image: ghcr.io/youyo/focal:latest
    network_mode: "service:idproxy"   # idproxy のネットワーク namespace を共有する
    command: ["serve", "--listen", "127.0.0.1:8080", "--identity", "default=/keys/id_ed25519"]
    environment:
      FOCAL_UPSTREAM_TOKEN: "<32バイト以上の可視ASCII。プレースホルダ。実値は secret 管理に置く>"
    volumes:
      - ~/.ssh:/root/.ssh:ro
      - ./keys:/keys:ro
    # focal はループバックにしか bind しないため、idproxy と同じネットワーク namespace か
    # 同一 Pod で動かす（別ホストに分ける場合は non-loopback bind が必要になり、token が
    # あっても通信は平文なので前段に TLS 終端プロキシを置く）
```

idproxy 側は focal からの `401` をそのまま呼び出し元へ素通しするのではなく、idproxy 自身の
認証エラーとして正規化して返すことが期待されます（focal の内部エラー文言をそのまま外部に
漏らさないため）。

Claude Desktop 側の接続手順の骨子:

1. Claude Desktop の設定でカスタムコネクタとして idproxy の `EXTERNAL_URL` を追加する。
2. ブラウザで OIDC 認証を完了する（idproxy が Bearer トークンを発行する）。
3. 以降 Claude Desktop から idproxy 経由で `inspect_*` ツールが呼び出せる。

**`focal serve` は token なしでは TCP に待ち受けません。** idproxy を前段に置く構成では、`focal serve` を
UDS（方式A）か、token 付きのループバック（方式B）で起動し、外部への公開は idproxy 側が担います。

## セキュリティモデル

Agent が制御できるのは次の3つだけで、実際に実行される Linux コマンドそのものは一切制御できません。

- **host** — どのサーバーに接続するか
- **operation** — `system` / `logs` などどの built-in operation を使うか
- **型付き parameter** — その operation が明示的に公開した `--since` や `SERVICE` のような値

原則は2本柱です。

1. **Focal never accepts arbitrary remote commands.**
2. **Focal does not replace SSH configuration. It constrains what may be executed through SSH.**

これを支える具体的な性質:

- `-o`（`ProxyCommand` 等の ssh_config オプションを渡す経路）は存在しません。
- `focal HOST exec` / `run` / `shell` / `command` に相当する operation はありません。CLI・MCP どちらも
  operation 名は固定テーブルから解決され、未知の名前は拒否されます。
- `kernel` operation は既定で **disabled** です。有効化しない限り、CLI からも MCP からも呼び出せません。
- 実際にリモートへ渡る argv のプログラム名・オプションは Focal 自身のコード中の文字列リテラルであり、
  Agent が渡せる値は value object（`ServiceName` / `PackageName` / `Duration` / `LineLimit` / `PID` / `ProcessName` /
  `Target` 等）の検証を経てから、決められたトークンの位置にのみ挿入されます（`internal/ssh` の
  argv 組み立て、[ADR 0002](docs/adr/0002-command-construction-boundary.md)）。
- `transport`（`ssh` | `ssm`）は Agent が呼び出しごとに選べますが、実行できる operation の集合を
  一切広げません。SSM transport が組み立てる1行のシェル文字列も、SSH と同じ value object 検証済みの
  `ssh.Command` だけから構成され、設定ファイルで明示的に有効化しない限り呼び出せません
  （[ADR 0007](docs/adr/0007-ssm-transport.md)）。

## ADR

設計上の主な判断は `docs/adr/` に記録しています。

- [0001. `.ssh/config` の解釈を OpenSSH に完全委任する](docs/adr/0001-delegate-ssh-config-to-openssh.md)
- [0002. Command 型の生成境界と sudo 判断の一点集約](docs/adr/0002-command-construction-boundary.md)
- [0003. YAML ライブラリに goccy/go-yaml を採用する](docs/adr/0003-yaml-library-selection.md)
- [0004. Remote MCP でも `.ssh/config` の解釈を OpenSSH に完全委任する](docs/adr/0004-delegate-ssh-config-in-remote-mcp.md)
- [0005. MCP SDK に modelcontextprotocol/go-sdk を採用し stateless Streamable HTTP で提供する](docs/adr/0005-mcp-sdk-and-stateless-transport.md)
- [0006. E2E テストは build tag 隔離 + docker compose + go-sdk クライアントで構成し CI 別ジョブで実行する](docs/adr/0006-e2e-test-strategy.md)
- [0007. AWS SSM Run Command を第二の transport として追加する](docs/adr/0007-ssm-transport.md)
- [0008. stdio で MCP を提供する `focal mcp` を追加し、`focal serve` の TCP 待ち受けでは token を必須にする](docs/adr/0008-stdio-transport-and-serve-auth-default.md)
- [0009. journal は新しい側から取り、Focal 側でエントリ単位に古い順へ戻す](docs/adr/0009-journal-read-order.md)
- [0010. package operation: rpm を先に試し、dpkg-query は終了コードだけで選ぶ](docs/adr/0010-package-operation.md)
- [0011. logs の絞り込み・期間の終わりと絶対時刻・時間ごとの集計](docs/adr/0011-logs-filter-and-window.md)

## ROADMAP（v0.1 スコープ外）

- **plugin 型 operation** — 任意 shell ではなく、構造化された `CommandSpec` を返す形で operation を
  拡張できる仕組み
- **OS 差分吸収** — systemd 以外（OpenRC 等）や Docker のような別のサービスマネージャへの対応
- **network operation の分割** — `ports` / `routes` / `dns` のような、より粒度の細かい operation への分割
- **中央 broker 構成** — 複数の `focal serve` をまとめて管理する構成
- **SSM transport の S3 出力対応** — AWS 側の固定上限（stdout 24000 / stderr 8000 文字）を超える
  完全な出力を S3 経由で取得できるようにする
- **SSM の SDK config を serve プロセスで共有** — 現状 `ssm.New`（`config.LoadDefaultConfig` と
  IMDS リージョン解決を含む）は CLI の1回の実行、または `focal serve` の tool call ごとに
  毎回呼ばれる。`focal serve` プロセス起動時に一度だけ解決して使い回すようにする

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
