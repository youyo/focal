# 0008. stdio で MCP を提供する `focal mcp` を追加し、`focal serve` の TCP 待ち受けでは token を必須にする

## ステータス

Accepted

本 ADR は [ADR 0004](0004-delegate-ssh-config-in-remote-mcp.md) と
[ADR 0005](0005-mcp-sdk-and-stateless-transport.md) の**追補**であり、どちらも置き換えない。
次の 2 つは引き続き有効である。

- 0004 の決定: `~/.ssh/config` の解釈を OpenSSH に完全委任し、鍵は事前登録した alias でのみ選ばせる
- 0005 の決定: MCP の実装に `modelcontextprotocol/go-sdk` を採用し、HTTP では stateless Streamable HTTP で提供する

本 ADR はそこに次の 2 つを加える。

- MCP の第二の入口として stdio を追加する
- `focal serve` の TCP 待ち受けで upstream token を必須にし、`--allow-unauthenticated-listen` を廃止する

## コンテキスト

### `focal serve` の待ち受けの現状（本 ADR の PR2 まで）

`focal serve` は既定で `127.0.0.1:8080` に待ち受け、呼び出し元を何も確かめずに応答する。
loopback 以外のアドレスに待ち受けるには、upstream token（`FOCAL_UPSTREAM_TOKEN`）を設定するか、
`--allow-unauthenticated-listen` を指定する必要がある。呼び出し元との間に境界を引く手段は 2 つある。

- `--listen unix:/path` の Unix ドメインソケット。モード 0600 で作られる
- upstream token。`Authorization: Bearer` で照合する（`internal/cli/auth.go`）

token の渡し方は、PR #25 で環境変数 `FOCAL_UPSTREAM_TOKEN` だけにした。`--upstream-token` フラグは
廃止済みである。`focal serve` は起動時に token を 1 回だけ読み、自分の環境から取り除く。
同じ PR で、token なしの loopback TCP に待ち受けるときの警告も追加した（`warnLoopbackOpenToLocalCallers`）。

### loopback が防がないもの

loopback で防げるのは、別ホストからの到達だけである。同じマシン上の別ユーザー・別プロセスは
`127.0.0.1:8080` に接続でき、運用者の SSH 鍵と AWS 認証情報を使って検査を実行できる。
既定の起動方法がそのまま「同じマシンの誰もが使える窓口」になっていて、警告を出しても既定の安全性は
変わらない。

### 前段の認証プロキシ（idproxy）の対応状況

focal は利用者を認証せず、前段に [idproxy](https://github.com/youyo/idproxy) のような認証プロキシを
置く前提である。idproxy は v0.10.2（2026-08-25）で次の 2 つに対応した。

- Unix ドメインソケットの上流への接続（[youyo/idproxy#33](https://github.com/youyo/idproxy/issues/33)）
- 上流への固定の `Authorization` ヘッダーの付与（[youyo/idproxy#34](https://github.com/youyo/idproxy/issues/34)）

つまり、idproxy を前段に置く構成は、token なしの TCP に頼らずに組める。

idproxy は、受け取った `Host` ヘッダーをそのまま上流へ転送する。一方で go-sdk v1.7.0 の
`StreamableHTTPHandler` は、接続の受け側アドレスが loopback で、`Host` が loopback でないリクエストを
403 で拒否する（DNS rebinding 対策）。この 2 つから、loopback の TCP に待ち受ける focal の前段に
idproxy を置くと、token が正しくても 403 になりうると推論できる。これはまだ実測していない
（PR2 の承認条件で確かめる）。

### ローカルの単一クライアントという使い方

Coding Agent がローカルで MCP サーバーを使うときの最も一般的な形は stdio である。クライアントが
サーバーを子プロセスとして起動し、stdin/stdout で JSON-RPC をやり取りする。focal には現在 HTTP の入口しか
ないので、手元の Agent 1 つから使うだけでも、待ち受け口を開けなければならない。

### go-sdk v1.7.0 の stdio の挙動（実測）

go-sdk v1.7.0（`go.mod`）の `StdioTransport` / `IOTransport` を、子プロセスとして起動して確かめた。
これは SDK 単体での実測であり、focal に組み込んだ形や、実在のクライアントとの組み合わせはまだ検証していない。

- **Stateless のような設定はない。** stdio は 1 プロセスで 1 セッションになる。
  SDK が対応するプロトコル版（2026-07-28 / 2025-11-25 / 2025-06-18 / 2025-03-26 / 2024-11-05）は
  すべて有効で、`server/discover` も 5 版すべてを返した。
- **2 つの流儀を同時に受け付ける。**
  - `initialize` を送る従来の流儀: `initialize` より前に届いた `tools/list` / `tools/call` は、
    JSON-RPC エラー（code 0、`method "tools/list" is invalid during session initialization`）になる。
    プロセスは落ちずに続く。`notifications/initialized` は実際には要求されない。
  - リクエストごとに `params._meta` を付ける流儀:
    `io.modelcontextprotocol/protocolVersion` が `2026-07-28` 以上で、
    `io.modelcontextprotocol/clientCapabilities` も付いていれば、`initialize` なしで
    `tools/list` / `tools/call` が成功する。この流儀で `initialize` を送ると MethodNotFound になる。
- **`initialize` で返る版は、従来の流儀では 2025-11-25 が上限になる。** クライアントの指定と応答は次のとおり。
  - 2026-07-28 → 2025-11-25
  - 2025-11-25 / 2025-06-18 / 2025-03-26 → 指定と同じ版
  - 未知の版 → 2025-11-25
- **stdin が EOF になると `Run` は nil を返す。** 実行中の呼び出しがあった場合は、
  `server is closing: EOF` を返す。このときツールハンドラの ctx はキャンセルされ、
  `exec.CommandContext` で起動した ssh(1) は止まる。
- **stdin に JSON でない行が届くと、`Run` はエラーを返して終了する。** 以降は応答しない。
- **`Run` に渡した ctx をキャンセルすると、`Run` はセッションを閉じて `ctx.Err()` を返す。**
- **`tools/call` は並行に処理される。** `Server.AddReceivingMiddleware` で入れたセマフォは
  同時実行数を制限でき、待っている間も入力の読み取りは止まらない。

0005 と `internal/mcp/server.go` のコメントにある「initialize もセッションもない」は、Streamable HTTP の
経路についての記述である。stdio には当てはまらない。

focal 側に stdout へ書く経路があるかも確かめた。MCP を提供している間に focal のコードが stdout に書く経路は
見つからなかった。

- ssh(1) の子プロセスは、stdout / stderr を focal 内のバッファに受けている
- ssh(1) の子プロセスの stdin は既定の /dev/null で、`-n` も付けている
- `internal/ssm` は出力しない
- SDK のロガーは既定で出力を捨てる

## 決定

### 1. stdio は別サブコマンド `focal mcp` で提供する

`focal mcp [--identity alias=path]... [--user USER] [--config PATH]` として追加する。
`--identity` / `--user` / `--config` の意味は `focal serve` と同じである。待ち受けに関するフラグ
（`--listen` など）は持たない。

`focal serve` のオプションとして足さず、別のサブコマンドにする理由は次のとおり。

- `serve` のフラグの半分は stdio では意味を持たない。
- stdio は `serve` の待ち受け方針（アドレスの判定・ソケットの検査・token）を一切通らない。
  これを、フラグの組み合わせではなくコマンドの構造で示せる。
- クライアントの設定が `command: focal`、`args: ["mcp"]` だけで済む。

### 2. ツール登録・runner・config の経路は `focal serve` と共有する

`internal/mcp` では、`NewHandler` からツールの登録（`sdk.Server` の構築）を切り出す。HTTP の入口と
stdio の入口は、同じ構築関数を通る。stdio の入口は `Run` の呼び出しまで `internal/mcp` の中に持つ
（例: `ServeStdio(ctx, cfg, runner, opts, in, out)`）。こうすれば `internal/cli` が go-sdk を import しない
現在の性質を保てる。

`internal/cli` では、設定ファイルの読み込み、identity alias の登録、`serveRunner` の構築を
`focal serve` と共通の関数にまとめ、両方のコマンドから使う。

その結果、次のものは stdio を加えても変わらない。

- リクエストの一本道: `operation.Registry.Build` → `internal/vo` → `policy.Resolve` →
  `internal/ssh` の factory → `ssh.Executor`
- transport（`ssh` / `ssm`）の選択と `execution.transports` による opt-in
- 0002 の Command 生成境界とガードテスト、golden file
- `internal/mcp` が `internal/ssh` を import しない制約（0004）

### 3. stdout は JSON-RPC 専用にし、ログと警告は stderr にだけ出す

`focal mcp` が MCP を提供している間（`Run` に入ってから終了するまで）、stdout に書かれるのは SDK が書く
JSON-RPC メッセージだけにする。focal の診断と警告はすべて stderr に出す。

起動時に拒否する場合（設定ファイル・identity の誤り、未知のフラグなど）は、既存のコマンドと同じ
`result.Error` の JSON を stderr に書き、stdout には何も書かずに終了する。

`focal mcp --help` の出力は MCP の提供が始まる前なので、ほかのコマンドと同じく cobra が stdout に書いてよい。
クライアントが `--help` を付けて起動することはない。

### 4. 同時実行の上限を stdio にも掛ける

stdio の入口が構築した `sdk.Server` に `Server.AddReceivingMiddleware` で、`tools/call` だけを数える
セマフォを入れる。`tools/list` などは軽いので数えない。上限は `focal serve` と同じ 8 である
（`serveMaxInFlight` と同じ値を 1 か所で定義する）。

待っている呼び出しは、ctx が終わった時点で実行せずに諦め、JSON-RPC エラーを返す。
HTTP で 503 を返すのに相当する。stdio は接続が 1 本なので、この上限はそのまま 1 クライアントの上限になる。

HTTP 側の `limitInFlight`（http.Handler）は PR1 では触らない。middleware は stdio の入口にだけ掛け、
HTTP 側に二重の上限を作らない。

### 5. `focal serve` の TCP 待ち受けは、アドレスによらず token を必須にする

loopback を含め、TCP に待ち受けるときに `FOCAL_UPSTREAM_TOKEN` が設定されていなければ、起動を拒否する。
既定のアドレス `127.0.0.1:8080` は変えないが、token がなければ起動しない。
拒否するときのエラーの `allowed` には、次の 3 つを挙げる。

- `FOCAL_UPSTREAM_TOKEN`
- `--listen unix:/path`
- `focal mcp`（ローカルの単一クライアントで使う場合）

Unix ドメインソケットは例外とし、token がなくても起動できる。境界はソケットの 0600 が引くからである。
token が設定されていれば、ソケットでも従来どおり token を要求する。

起動を拒否するかどうかは、token の有無だけで決める。token のない TCP の待ち受けが無くなるので、
token なしの loopback に向けた警告（`warnLoopbackOpenToLocalCallers`）も無くなる。
`listensOnLoopback` は、token ありで loopback 以外に待ち受けるときの平文の警告を出すかどうかの判定にだけ残る。

### 6. `--allow-unauthenticated-listen` は PR2 で廃止する

フラグは削除し、指定されたら未知のフラグとして使い方の誤り（exit 2）にする。
これまでの用途はどれも、ほかの手段で置き換えられる。

- 前段の idproxy は v0.10.2 から、Unix ドメインソケットへの接続にも、上流への `Authorization` の付与にも
  対応している。
- ローカルの単一クライアントは `focal mcp` を使う。

### 7. token は自動生成しない

focal が token を生成すると、それを運用者に渡す経路は stderr しかない。stderr はログに残るので、
生成した秘密がそのままログに残る。token は運用者が生成し（`openssl rand -hex 32` など）、
secret 管理から環境変数で渡す。

### 8. token をコマンドライン引数で渡す経路は持たない

PR #25 で `--upstream-token` を廃止済みである。コマンドライン引数はプロセス一覧から同じマシンの
別ユーザーにも見え、本 ADR が防ごうとしている相手にそのまま渡ってしまう。
ファイルで渡す方法（`--upstream-token-file` など）は、必要な配備が出てくるまで作らない。

### 9. token を環境から外す処理は、全コマンドが通る 1 か所に置く（PR1）

`FOCAL_UPSTREAM_TOKEN` を読んで環境から取り除く処理（今の `upstreamToken`）を、`focal serve` の中から、
全コマンドが通る入口（`app.run` の先頭など）の 1 か所に移す。`focal serve` はそこで読んだ値を受け取る。

- 理由: 今は `focal serve` だけが取り除いていて、`focal HOST OPERATION` と `focal mcp` では環境に残る。
  残った値は起動する ssh(1) に引き継がれ、運用者の `~/.ssh/config` に `SendEnv FOCAL_*` があれば
  検査対象のホストに送られる。
- `focal mcp` では、値が設定されていれば、stdio では使わないことを stderr に知らせる。
- 限界: `os.Unsetenv` が防ぐのは、子プロセスへの継承だけである。OS が公開するプロセスの初期環境
  （Linux の `/proc/<pid>/environ` など）からは消えず、同じ UID のプロセスはそこから値を読める。

### 10. `focal mcp` の終了コードと終了までの時間

| 終わり方 | 終了コード |
|---|---|
| stdin の EOF（`Run` が nil を返した場合も、`server is closing: EOF` を返した場合も） | 0（`exitOK`） |
| SIGINT / SIGTERM（`signal.NotifyContext` で ctx をキャンセルし、実行中の処理を止めてから） | 0（`exitOK`） |
| 不正な入力行などで `Run` がエラーを返したとき（`result.Error` を stderr に書く） | 1（`exitRejected`） |
| 起動前の拒否 | 既存どおり 1 / 2 |

- 理由: stdio の正規の終了手順は「クライアントが stdin を閉じ、待ってから SIGTERM、最後に SIGKILL」である。
  go-sdk のクライアントは、子プロセスの終了状態をそのまま `Close` のエラーとして返す。正規の終了で
  0 以外を返すと、クライアントからは失敗に見える。
- EOF やシグナルを受けてから終了するまでの時間には、全体で 5 秒未満の上限を設ける。上限を過ぎて残っている
  処理は待たずに終了する。go-sdk のクライアントは stdin を閉じてから 5 秒待って SIGTERM を送るので、
  それより短くする。個々の処理はすでに止まる仕組みを持っている。
  - ssh(1) の子プロセスは、ctx のキャンセルで `exec.CommandContext` によってすぐ SIGKILL される。
    待つのは `WaitDelay`（2 秒、`internal/ssh/openssh.go`）までである。
  - SSM は、ctx のキャンセルで `CancelCommand` を送る。
- focal には今シグナルの処理がない。`focal serve` もシグナルを ctx に変換しておらず、`serveOn` の
  終了処理はシグナルでは動かない。`focal serve` にも入れるかは、本 ADR では決めない。
- クライアントが stdout を先に閉じた場合は、Go の既定の動作で SIGPIPE によって終了する。特別には扱わない。

### 11. stdin/stdout は `cli.Execute` が受け取ったものを `sdk.IOTransport` で使う

`sdk.StdioTransport`（`os.Stdin` / `os.Stdout` を直接使う）は使わない。どのストリームを使うかを決める場所が
`cmd/focal` の 1 か所に保たれ、プロセスの中で動くテストからも同じ経路を試せる。

`os.Stdout` を起動時に `os.Stderr` へ差し替える防御は入れない。理由は次の 2 つ。

- パッケージ変数を書き換えるので、同じプロセス内のテストに干渉する
- Go の変数を差し替えても、fd 1 に直接書く経路は防げない

stdout の汚れは、承認条件の回帰テストで検出する。依存に stdout へ書くものが入ったときに見直す。

### 12. プロトコル版は SDK に任せる

`initialize` を送る流儀（2025-11-25 以下で合意する）と、2026-07-28 の `_meta` の流儀の両方を SDK に任せる。
focal 側で版を絞ったり書き換えたりはしない。0005 は JSON-RPC とプロトコルの扱いを SDK に委ねると
決めており、版を絞るには JSON-RPC を focal が読む middleware が必要になって、その決定に反する。

E2E では、主要なクライアントが使う `initialize` の流儀を必ず通す。`_meta` の流儀は unit テストで確かめる。

### 13. `internal/mcp/server.go` のコメントを PR1 で直す

次の 2 か所を、「Streamable HTTP の経路について」と明記する形に直す。stdio ではセッションがあり、
版は SDK が決めることも書く。今の文言は、stdio を加えると誤りになる。

- `NewHandler` 内の「Stateless is not a tuning knob ...」
- package doc の「The server is stateless ...」の段落

### 14. focal 自身の `Host` 検査は足さない

DNS rebinding への対策として、focal が `Host` ヘッダーを待ち受けアドレスに限定する検査は持たない。

- 決定 5 で全 TCP が token 必須になれば、token を持たないブラウザは 401 で止まる。
- `Host` を限定すると、idproxy が転送する外部名の `Host` を持つ正規のリクエストまで拒否してしまう。

### 実装の順序

1. **PR1**: `focal mcp` を追加する（決定 1〜4、9〜13）。`focal serve` の挙動は変えない。
2. **PR2**: token のない TCP の待ち受けをやめ、`--allow-unauthenticated-listen` を廃止する（決定 5・6）。
   README、`focal serve` のヘルプ、エラーの文言も合わせて直す。

stdio を先に用意するのは、PR2 で起動を拒否するときに「ローカルの単一クライアントなら、token を用意せずに
`focal mcp` を使える」という代わりの手段を、エラーの中で示せるようにするためである。

### PR1（stdio）の承認条件

PR1 は次の 2 つを E2E（[ADR 0006](0006-e2e-test-strategy.md) の `test/e2e`、`e2e` build tag）で
満たしたときに承認する。

- **`tools/list` と `tools/call` が通る。** ビルドした実バイナリを子プロセスとして `focal mcp` で起動する。
  クライアントは 0006 と同じく go-sdk のクライアント（`CommandTransport`）を使う。
  `tools/call` は E2E の sshd コンテナに対して、実際に ssh(1) を実行する。
- **stdout が汚れないことの回帰テストが通る。** 実バイナリに改行区切りの JSON-RPC を直接書き込み、
  stdout の全行が JSON-RPC 2.0 のメッセージとして読めることを確かめる。
  - ssh(1) を実際に動かす `tools/call` の後でも成り立つこと
  - 起動時に拒否されたとき（設定ファイルの誤り、未知のフラグなどの使い方の誤り）は stdout が空であること

  stdio は改行区切りの JSON なので、JSON-RPC を自前で書いても、0006 が HTTP について退けた脆さ
  （SSE、必須ヘッダー）は生じない。

同時実行の上限と `ServeStdio` 自体は、SDK のインメモリ transport を使った unit テストで確かめる。

加えて、実在する主要な MCP クライアント（Claude Code など）の少なくとも 1 つで、接続を手動で確認する。
E2E の go-sdk クライアントと実在のクライアントでは、送る内容が同じとは限らないからである。手順は次のとおり。

1. クライアントの設定に、`command: focal`、`args: ["mcp", "--identity", "default=..."]` で focal を登録する。
2. focal を直接ではなく、stdin と stdout をそれぞれファイルにも写す薄いラッパー（`tee` など）経由で起動する。
3. クライアントでツール一覧に `inspect_*` が並ぶこと、1 つのツールの呼び出しが結果を返すことを確かめる。
4. 記録から、クライアントが使った流儀（`initialize` か `_meta` か）と、合意したプロトコル版を記録する。
5. クライアントを終了し、focal のプロセスが残らないことを確かめる。

確認したクライアントの名前と版、観測した流儀、合意した版は PR の説明に書く。

### PR2 の承認条件

PR2 は、上記の推論（loopback の TCP の前段に idproxy を置くと 403 になりうる）を実測してから承認する。

- 外部名の `Host` と正しい `Authorization: Bearer` を付けたリクエストを、loopback の TCP に待ち受けた
  `focal serve` と、Unix ドメインソケットに待ち受けた `focal serve` の両方に送り、結果を記録する。
  この確認は E2E に入れる。
- loopback の TCP で 403 になった場合に SDK の検査を外すかどうかは、実測の結果を見てから判断し、
  本 ADR の追補として記録する。
- 403 を確認した場合は、README の方式 B（idproxy と同じネットワーク namespace で `127.0.0.1:8080` に
  待ち受ける構成）を直す。

## 脅威と限界（運用上の説明）

PR2 以降の各手段について、誰からの到達を防げるかを次にまとめる。

| 手段 | 別ホスト | 同じマシンの別ユーザー | 同じ UID の別プロセス | 運用者のブラウザ経由 |
|---|---|---|---|---|
| TCP + token | 防ぐ（loopback 以外では平文） | 防ぐ（token を知らない限り） | 防がない | 防ぐ（token がなく 401） |
| Unix ドメインソケット（0600） | 防ぐ | 防ぐ | 防がない | 防ぐ（ブラウザは接続できない） |
| stdio（`focal mcp`） | 防ぐ | 防ぐ | 待ち受け口はないが、信頼境界ではない（下記） | 防ぐ（待ち受け口がない） |

PR2 までの現状では、token なしの TCP の待ち受けがありうる。その場合、同じマシンの別ユーザー・別プロセスは
到達できる。運用者のブラウザからのリクエストは次の 3 つの検査で止まる。

- SDK の `Host` 検査（接続の受け側アドレスが loopback の場合だけ効く）
- `http.NewCrossOriginProtection`（`internal/cli/serve.go` の `serveHandler`）
- SDK の Content-Type 検査（単純な POST を 415 で拒否する）

ただし、`--allow-unauthenticated-listen` で loopback 以外に待ち受けると、`Host` 検査が効かない。
そのアドレスに向け直す DNS rebinding は、ブラウザから見ると同一オリジンになるので、残りの 2 つの検査も
通り抜ける。PR2 で token のない TCP の待ち受けを無くすことで、この経路も無くなる。

### 同じ UID の別プロセス

同じ UID の別プロセスを token や 0600 ソケットで防げない理由は次のとおり。

- **token:** focal は起動後に token を自分の環境から取り除くが、OS が公開するプロセスの初期環境は
  消せない（決定 9）。また、前段プロキシの設定にも同じ token が書かれている。同じ UID のプロセスは、
  どちらからでも token を読める。
- **0600 ソケット:** パーミッションは所有者を通す。同じ UID のプロセスはソケットにそのまま接続できる。

stdio には待ち受け口がない。そのぶん、アドレスを知っているだけのプロセスやブラウザが偶然、
あるいは気軽に focal に届くことはない。正規の経路で focal と話せるのは、focal を起動して stdin/stdout の
パイプを持っている親プロセスだけである。

ただし、stdio でも同じ UID は信頼境界ではない。同じ UID のプロセスは、次のことができる。

- Linux では `/proc/<pid>/fd/0`・`/proc/<pid>/fd/1` からパイプを開き直す。これで JSON-RPC の行を
  注入したり、応答を横取りしたりできる。
- ptrace でプロセスに介入する。これをどこまで許すかは `kernel.yama.ptrace_scope` などの OS の設定による。
- 不正な行を注入する。stdio は不正な入力行で終了するので（コンテキスト参照）、それだけで focal を
  止められる（サービス妨害）。
- そもそも focal を自分で起動したり、運用者の SSH 鍵や AWS 認証情報を直接使ったりする。

同じ UID の中を分けたいなら、focal を専用の OS ユーザーで動かすなど、OS のユーザー分離を使う。

正規の経路で stdio の入力を書けるのは起動した親プロセスだけで、親は信頼境界の内側にある。したがって、
不正な入力行で `focal mcp` が終了する SDK の挙動は、そのまま受け入れる。同じ UID からの注入で止められる
ことは、同じ UID を信頼境界にしない以上、防ぐ対象にしない。

## 検討した代替案

- **`focal serve --listen stdio` のように、serve のオプションとして stdio を足す**:
  待ち受けのフラグと待ち受け方針のコードが、stdio では意味を持たないまま同じ経路に混ざる。不採用。
- **`--allow-unauthenticated-listen` を残し、「この TCP の待ち受けで focal は呼び出し元を確かめない」という
  運用者の明示として意味を改め、使われたら警告する**: 不採用。理由は次のとおり。
  - 残す根拠だった「前段の idproxy が Unix ドメインソケットにも token の付与にも対応していない」は、
    idproxy v0.10.2 で事実でなくなった。
  - 警告は読まれないことがある。
  - loopback での用途（同じマシンの idproxy から token なしで受ける）は、SDK の `Host` 検査で 403 になり、
    そもそも成り立たない可能性がある。
- **focal 自身で `Host` を待ち受けアドレスに限定する**: 決定 14 のとおり、token 必須で足り、
  idproxy 経由の正規のリクエストまで拒否するため不採用。
- **接続元のプロセスの資格情報（Linux の `SO_PEERCRED`、darwin の `LOCAL_PEERCRED`）で呼び出し元の
  UID を確かめる**: Unix ドメインソケットでしか使えず、OS ごとに API が違い、loopback の TCP では
  得られない。Unix ドメインソケットで使えたとしても、0600 がすでに別ユーザーを防いでいる。
  同じ UID は同じ UID として見えるので、0600 以上の境界にはならない。コードだけが増えるため不採用。
- **OAuth 2.1 を focal 自身に実装する**: ユーザーの認証は前段の認証プロキシ（idproxy）の責務である。
  focal に入れると、認可を誤りうる場所が 2 つになる（`internal/cli/auth.go` 冒頭の方針）。不採用。
- **token を自動生成して表示する**: 決定 7 のとおり、生成した秘密がログに残る。不採用。
- **token をファイルで渡す（`--upstream-token-file`）**: 今はその要件がないので見送る。要件が出たら再検討する。
- **token をフラグでも受け取る（PR #25 以前の状態）**: 決定 8 のとおり、引数がプロセス一覧から
  見えるため不採用。
- **既定はそのままにして、警告と文書だけで済ませる**: ローカルの単一クライアント向けには stdio が
  用意できる。安全でない既定を残す理由がなく、警告は読まれないこともあるので不採用。
  既定はそのままで警告と文書だけを足したのは、PR #25 までの暫定の対応である。

## 影響

- PR2 以降、token なしの `focal serve`（Unix ドメインソケット以外）は起動しない。
  `--allow-unauthenticated-listen` を指定していた起動も、未知のフラグとして失敗する。
  既定の挙動の非互換な変更だが、v0.x の段階であり、互換性は保たないと判断した。
- PR2 で直す対象は次のとおり。
  - README の起動例と MCP 節の説明
  - `focal serve` の `Long` ヘルプとフラグの説明
  - `internal/cli/serve.go` 冒頭のコメントと `defaultListenAddress` のコメント
  - `unauthenticated_listen_address` エラーの文言と `allowed`
  - `allowBeyondLoopback` のフラグの分岐と `warnLoopbackOpenToLocalCallers`（token なしの経路が無くなる）
  - 関連する unit テストと E2E
  - README に、systemd や docker compose で `focal serve` を token なしの loopback で動かしていた構成の
    移行例を載せる。Unix ドメインソケットに移す例と、`FOCAL_UPSTREAM_TOKEN` を secret 管理から渡す例の 2 つ
- `focal mcp` の stdin を扱うために、`cli.Execute` と `app` が stdin を受け取るようにする
  （`cmd/focal/main.go`、`cmd/focal/run.go` も変わる）。リポジトリ内部の契約の変更であり、外部には公開しない。
- PR1 以降、`focal HOST OPERATION` も起動時に `FOCAL_UPSTREAM_TOKEN` を環境から取り除く（決定 9）。
- `mcp` という名前のホストは、サブコマンドの陰に隠れる。今の `serve` と同じで、たとえば
  `focal serve system` は `serve` サブコマンドとして解釈され、使い方の誤りとして拒否される。
  `user@mcp` と書くか、`~/.ssh/config` で別名を付ければ回避できる。
- 0002 の境界、ガードテスト（`internal/ssh/boundary_test.go`、`internal/security_test.go`、
  `internal/operation/registry_test.go`）、golden file は変わらない。
- 0005 の「`initialize` もセッション ID もない」は、Streamable HTTP の経路についての決定として残る。
  stdio は 1 プロセス 1 セッションで、どのプロトコル版で話すかは SDK が決める。
- E2E（0006）に stdio の経路と、PR2 の `Host` の実測が加わる。docker の要件は変わらない。

## 再検討の条件

- stdio が期待どおりに動かないとき、または主要な MCP クライアントとプロトコル版が合わないとき
- go-sdk の更新で、stdio のプロトコル版の扱い、終了・不正入力のときの挙動、`Host` 検査の条件が変わったとき
- token をファイルで渡す必要のある配備の要件が出たとき
- Unix ドメインソケットへの接続も、上流へのヘッダーの付与もできない前段プロキシを使う配備が、
  具体的に出てきたとき
- 既定の挙動に依存する利用者が現れたとき（それ以降の既定の変更は、互換性を考えて行う）
- Unix ドメインソケットに直接つなげる MCP クライアントが出たとき

## 追補: PR2 の実測と、token があるときの `Host` 検査

### 実測の結果

PR2 の承認条件のとおり、外部名の `Host`（`focal.example.com`）と正しい `Authorization: Bearer` を付けた
`tools/list` を、実バイナリの `focal serve` に送った（`test/e2e/host_test.go`）。変更前の結果は次のとおり。

| 待ち受け | `Host` | 結果 |
|---|---|---|
| loopback の TCP（`127.0.0.1:<port>`）、token あり | `127.0.0.1:<port>`（対照） | 200 |
| loopback の TCP（`127.0.0.1:<port>`）、token あり | `focal.example.com` | 403 `Forbidden: invalid Host header "focal.example.com"` |
| Unix ドメインソケット、token あり | `focal.example.com` | 200 |
| Unix ドメインソケット、token なし | `focal.example.com` | 200 |

403 は go-sdk v1.7.0 の `StreamableHTTPHandler.ServeHTTP`（`mcp/streamable.go`）が返していた。
接続の受け側アドレスが loopback で、`Host` が loopback でないリクエストを拒否する検査である。
token の照合は通ったうえで、その内側の SDK のハンドラーで拒否されていた。
Unix ドメインソケットでは受け側アドレスがソケットのパスになり、loopback と判定されないので、この検査は効かない。

コンテキストの推論どおり、loopback の TCP に待ち受ける focal の前段に、`Host` をそのまま転送する
idproxy を置くと、token が正しくても 403 になる。README の方式 B はこの構成に当たる。

### 決定: token の照合が掛かっているときは、SDK の `Host` 検査を外す

- 検査を外す条件は「token の照合（`auth.wrap`）が掛かっているとき」とする。「TCP で待ち受けるとき」ではない。
  `internal/cli` の `serveHandler` で、照合を掛けるかどうかを決める 1 か所から値を導き、別々に決めない。
  Unix ドメインソケットに token を設定したときも外れるが、ソケットはもともと検査の対象外なので挙動は変わらない。
- `internal/mcp.Options` には `DisableHostCheck bool` を足す。ゼロ値では検査が有効で、指定し忘れると
  安全側になる。`focal mcp`（stdio）は渡さない。
- 外す手段は `StreamableHTTPOptions.DisableLocalhostProtection` だけを使う。環境変数 `MCPGODEBUG` は使わない。
- `http.NewCrossOriginProtection` は残す。クロスサイトと名乗るブラウザのリクエストは、token があっても拒否する。
- 決定 14（focal 自身の `Host` 検査は足さない）は変わらない。

### token があるとき、検査が止めるものが残らない理由

`Host` 検査が防ぐのは、DNS rebinding でブラウザを loopback の待ち受けへ向ける攻撃である。
決定 5 で、TCP の待ち受けには必ず token が掛かる。rebinding されたブラウザは token を知らないので、
いちばん外側の照合で 401 になり、SDK のハンドラーまで届かない。token がある構成で `Host` 検査が
新たに止めるのは、token を持った正規のリクエスト（前段のプロキシが外部名の `Host` で転送するもの）だけになる。
ただしこれは、リクエストが focal に直接届く場合の話である。前段のプロキシが token を付ける経路は、
次の「残るリスク」で扱う。

### 残るリスク

前節の説明は、ブラウザのリクエストが focal に直接届く場合だけのものである。前段のプロキシが token を
付けて転送する場合は、token が漏れていなくても、検査が止めていた経路が開く。次の 3 つがすべて
成り立つとき、ブラウザが開いた別のサイトのリクエストを、プロキシが token 付きで focal へ転送する
（DNS rebinding）。

- プロキシに、ブラウザから平文の HTTP で届く。
- プロキシの利用者認証が、接続元だけで決まる。
- プロキシが `Host` を検証せずに転送する。

token の漏洩は、この経路の条件に入らない。token の秘匿（決定 7・8・9）が境界であることは変わらないが、
それだけでは前段のプロキシ経由の経路を閉じられない。

idproxy では、この経路は成り立たない。

- 利用者の認証は、Bearer の検証か、host-only・`SameSite=Lax` のセッション Cookie で行う。認証なしで
  上流に通す分岐は無い。
- `EXTERNAL_URL` は https が必須で、例外は `http://localhost` だけである。

SDK の `Host` 検査は、Unix ドメインソケットと loopback 以外の TCP の待ち受けにはもともと効いておらず、
それらの構成では、変更前から同じ条件で経路が開いていた（実測: Unix ドメインソケット・token なしで、
外部名の `Host` が 200）。今回、loopback の TCP の検査を外しても、効いていなかった範囲は変わらない。
loopback の TCP は、効いていた検査を外す点だけが新しい。

対策は、focal の検査ではなく、前段のプロキシへの要件である。要件は README の
「前段にプロキシを置くときの要件」に書く。

### 却下した案

- **前段のプロキシから loopback 以外のアドレスへ接続させ、検査を避ける**: 検査が効かないのはアドレスの
  偶然による。待ち受けを loopback の外に出すと平文の経路が広がり、構成ごとに検査の有無が変わる。不採用。
- **前段のプロキシとの間を Unix ドメインソケットだけにする**: 別ホスト・UID を揃えられないコンテナの
  構成（方式 B）が組めなくなる。不採用。
- **`--allowed-host` のようなフラグで許す `Host` を足す**: 前段のプロキシ経由の経路が前提になると、
  token があっても `Host` の限定には意味がある。それでも不採用とする。
  - 任意の設定にすると、前段を誤って構成する運用者ほど設定しない。守りたい相手に効かない。
  - 必須にすると、正しく構成している全員に、前段の外部名と focal の設定の二重管理を強いる。
- **検査を戻して、方式 B をやめる**: 方式 A（Unix ドメインソケット）とコンテナ間の構成の経路は、
  検査を戻しても閉じない。同じネットワーク namespace で UID を揃えられない構成も、取れなくなる。不採用。

### 再検討の条件

- go-sdk を更新したとき。`DisableLocalhostProtection` が外す範囲（`Host` 検査のほかに何かを外していないか）と、
  検査の条件を確かめる。
- token なしの TCP の待ち受けを戻すとき。その場合、検査は外さない。
- README が idproxy 以外の前段プロキシを案内するとき。
- 接続元だけで認証する前段のプロキシを置く配備の要件が、具体的に出てきたとき。
- idproxy が、認証なしで上流に通す経路か、`Domain` 属性付きの Cookie を持つようになったとき。
