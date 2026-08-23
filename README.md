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
| `processes` | `ps -eo pid,ppid,user,state,%cpu,%mem,etime,comm,args`（フィルタは remote に渡さず Focal 側でローカルに適用） | never | `--name`, `--pid` |
| `service` | `systemctl show UNIT --no-pager --property=Id,Description,LoadState,ActiveState,SubState,MainPID,ExecMainStatus,Result` | never | `SERVICE`（例: `nginx` → `nginx.service` に正規化） |
| `logs` | `journalctl -u UNIT --since=-DURATION -n LINES --no-pager --output=short-iso` | auto（`never` へ変更可） | `SERVICE`, `--since`（既定 `30m`）, `--lines`（既定 `200`） |
| `kernel` | `journalctl -k --since=-DURATION -n LINES --no-pager --output=short-iso` | auto（`never` へ変更可）。**既定 disabled** | `--since`（既定 `1h`）, `--lines`（既定 `200`） |
| `inspect` | remote command なし。`system` / `cpu` / `memory` / `storage` / `network` / `processes` を最大3並列で実行し結果を合成する composite operation | never | なし |

sudo モードは `never` / `auto` / `always` の3値ですが、`always`（起動から常時 `sudo -n` を付ける）を
選べる operation は現状ありません。`auto` は無権限で一度実行し、権限不足を示す終了コード
（1 / 13 / 77 / 126）が返ったときだけ `sudo -n` を付けて一度だけ再実行します。`inspect` を含め
すべての operation の sudo capability は `internal/policy` が operation ごとに固定しており、config
ファイルはその範囲を狭めることしかできません。

## Requirements

Focal は接続先への SSH 実行に、利用者の環境にインストール済みの OpenSSH クライアントをそのまま使います。**OpenSSH クライアント 10.1 以降を推奨します**(CVE-2023-51385 / CVE-2025-61984 は `ssh_config` の `%h` / `%u` 展開経由の間接的な injection 経路であり、最新の OpenSSH クライアントを使うことが多層防御の一段になります)。

## 設定ファイル

Focal は `$XDG_CONFIG_HOME/focal/config.yaml`（`XDG_CONFIG_HOME` 未設定なら
`~/.config/focal/config.yaml`）を起動時に一度読みます。ファイルが存在しない場合は、読み取り専用
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

## MCP（`focal serve`）

`focal serve` は CLI と同じ operation を、stateless Streamable HTTP（MCP spec `2026-07-28`）の
MCP tool として公開します。**focal 自身はユーザーを認証しません**（誰が呼んでいるかを判定する機構は
持ちません）。既定で `127.0.0.1` にのみ bind し、それ以外のアドレスへ bind するには
`--allow-unauthenticated-listen` を明示するか、後述の upstream token を設定する必要があります。

```sh
focal serve --listen 127.0.0.1:8080 \
  --user ec2-user \
  --identity default=~/.ssh/id_ed25519 \
  --identity customer-a=~/.ssh/customer-a.pem
```

focal と呼び出し元の間の境界を強化する手段が2つあります。どちらも単独で運用でき、併用もできます。

- **Unix domain socket**（`--listen unix:/run/focal/focal.sock`）— 境界をファイルシステムの
  パーミッションで引きます。socket は `0600` で作成されるため、reachable なのは focal を動かしている
  ユーザーだけで、別ホストからは原理的に到達できません。**`0600` が防ぐのは別ユーザーからのアクセスだけで、
  同一ユーザーの別プロセスからの接続は防げません。** UDS 経由の bind では
  `--allow-unauthenticated-listen` は不要かつ無関係です（ネットワーク到達性の判定そのものを行わないため）。
- **固定 shared secret**（`--upstream-token`、または `FOCAL_UPSTREAM_TOKEN`）— 境界をリクエストに載る
  値で引きます。token が設定されているリクエストにのみ `Authorization: Bearer <token>` を要求し、
  一致しなければ `401`（body は固定文字列、token・設定値・スタック情報は含みません）を返します。token は
  32 バイト以上の可視 ASCII が必要です。**`--upstream-token` はプロセス一覧から読めるため、
  `FOCAL_UPSTREAM_TOKEN` を推奨します**（`--upstream-token` を使うと起動時に stderr へその旨の警告が
  出ます。token 自体は出力されません）。upstream token が設定されている場合、非 loopback bind は
  `--allow-unauthenticated-listen` なしでも許可されますが、通信は暗号化されないため（token も応答も平文で
  流れます）、loopback 以外で bind するなら前段に TLS 終端プロキシを置いてください。

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
```

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

idproxy と focal の間（`UPSTREAM_URL`）は、既定では平文 HTTP かつ focal 側に認証がないため、
同一ホスト／同一信頼境界での利用を前提とします。この間を強化する方式を idproxy 側が
（[youyo/idproxy#33](https://github.com/youyo/idproxy/issues/33) /
[#34](https://github.com/youyo/idproxy/issues/34)）で対応するまでは、以下の focal 側設定を
入れても idproxy はまだ利用しません。対応後に有効になります。

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
      UPSTREAM_URL: unix:///run/focal/focal.sock   # idproxy #33/#34 対応後に有効
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
token はプレースホルダです。実値は `openssl rand -hex 32` 等で生成し、リポジトリにコミットしないでください。

```yaml
services:
  idproxy:
    image: ghcr.io/youyo/idproxy:latest
    ports:
      - "8443:8443"
    environment:
      EXTERNAL_URL: https://focal.example.com
      UPSTREAM_URL: http://focal:8080
      UPSTREAM_AUTH_TOKEN: "<FOCAL_UPSTREAM_TOKEN と同じ値>"   # idproxy 側が Authorization: Bearer として注入
    # OIDC プロバイダの設定等は idproxy 側のドキュメントを参照

  focal:
    image: ghcr.io/youyo/focal:latest
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

**`focal serve` は既定で `127.0.0.1` にのみ bind します。** ループバック以外のアドレスへ bind するには
`--allow-unauthenticated-listen` を明示するか upstream token を設定する必要があり、どちらもなければ
起動時エラーになります（安全側デフォルト）。idproxy を前段に置く構成では `focal serve` はループバックの
まま（または UDS で）起動し、外部への公開は idproxy 側が担います。

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
  Agent が渡せる値は value object（`ServiceName` / `Duration` / `LineLimit` / `PID` / `ProcessName` /
  `Target` 等）の検証を経てから、決められたトークンの位置にのみ挿入されます（`internal/ssh` の
  argv 組み立て、[ADR 0002](docs/adr/0002-command-construction-boundary.md)）。

## ADR

設計上の主な判断は `docs/adr/` に記録しています。

- [0001. `.ssh/config` の解釈を OpenSSH に完全委任する](docs/adr/0001-delegate-ssh-config-to-openssh.md)
- [0002. Command 型の生成境界と sudo 判断の一点集約](docs/adr/0002-command-construction-boundary.md)
- [0003. YAML ライブラリに goccy/go-yaml を採用する](docs/adr/0003-yaml-library-selection.md)
- [0004. Remote MCP でも `.ssh/config` の解釈を OpenSSH に完全委任する](docs/adr/0004-delegate-ssh-config-in-remote-mcp.md)
- [0005. MCP SDK に modelcontextprotocol/go-sdk を採用し stateless Streamable HTTP で提供する](docs/adr/0005-mcp-sdk-and-stateless-transport.md)

## ROADMAP（v0.1 スコープ外）

- **plugin 型 operation** — 任意 shell ではなく、構造化された `CommandSpec` を返す形で operation を
  拡張できる仕組み
- **OS 差分吸収** — systemd 以外（OpenRC 等）や Docker のような別のサービスマネージャへの対応
- **network operation の分割** — `ports` / `routes` / `dns` のような、より粒度の細かい operation への分割
- **中央 broker 構成** — 複数の `focal serve` をまとめて管理する構成

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
