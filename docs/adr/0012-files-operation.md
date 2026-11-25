# 0012. files operation: 許可したディレクトリのメタデータだけを、root だけが変えられる経路の上で一覧する

## ステータス

Accepted

この ADR は [ADR 0002](0002-command-construction-boundary.md)（Command 生成境界）と
[ADR 0007](0007-ssm-transport.md)（SSM transport）の決定を変えない。`internal/ssh` の `literalSpec` /
`argSpec` / `programSpec` も広げない。[ADR 0011](0011-logs-filter-and-window.md) で導入した `Envelope.items` を使う。

## コンテキスト

障害調査では「このディレクトリに何がいつからあるか、どれだけの大きさか」を知りたい場面が多い
（ログローテーションの確認、巨大ファイルの特定、設定ファイルの更新時刻など）。いまの Focal には、
ファイルシステムの一覧を返す operation がない。利用者の要望は次の 2 点である（issue #28）。

- 管理者が許可したディレクトリと、その配下のサブパスの一覧を見たい
- 見えるのは名前・種類・サイズ・更新日時だけにしたい。ファイルの中身は読まない

設計の目標は、許可していない場所が、**symlink を経由しては見えない**ことである。
一覧は「どのパスを見てよいか」という判断を要する。これまでの operation は、見る対象が OS の固定の状態
（`uptime`・`ps`・`systemctl` など）で、場所を決める入力が無かった。ここで初めて、呼び出し元が渡すパスと、
管理者が設定した許可リストの照合が安全の核になる。照合には 3 つの穴がある。

1. **symlink。** 許可ディレクトリの中、あるいは許可エントリへ至る途中の要素が symlink なら、
   `/var/log/app/x` のような許可内の文字列で、許可外のディレクトリを一覧できてしまう。
2. **確認と使用のあいだの差し替え（TOCTOU）。** 「symlink が無いこと」を確かめてから `ls` を走らせるまでの間に、
   途中の要素が symlink に差し替えられると、確認は意味を失う。差し替えに必要なのは、検査したパスの
   どれかの祖先ディレクトリへの書き込み権だけである。アプリのユーザー、デプロイ用のユーザー、
   アプリを侵害した者がこれに当たる。さらに、この差し替えは**繰り返して当てられる**。一覧に出るファイル名は
   書き込める者が決められ、`files` の結果は LLM にそのまま届くので、名前に指示を書いておけば呼び出しを誘発
   できる。SSM は常に root で動く（ADR 0007）ので、当たったときに見えるのは root が見える範囲
   （`/root`、`/home/*/.ssh` の名前とサイズなど）まで広がる。
3. **出力の解釈。** `ls` の出力は行ごとに 1 エントリだが、ファイル名は呼び出し元でも管理者でもない第三者が
   決める。名前に改行や空白や ` -> ` を入れれば、素朴に行と列で切る実装は、存在しない行・列を読んでしまう。
   リモートのロケールや環境変数（`BLOCK_SIZE` など）でも、出力の形が変わる。

本 ADR は、この 3 つの穴を塞ぐ方法と、塞げずに残るものを記録する。issue #29・#30（ファイルの中身を読む
operation）は、本 ADR の外枠（メタデータのみ）を超えるので、別の ADR で扱う。本 ADR の残余リスクは
#29・#30 へ引き継がない。

## 決定

### 設定: `operations.files.paths`

```yaml
operations:
  files:
    enabled: true
    paths:
      - /var/log
      - /etc/nginx
```

- `files` は既定で**無効**で、既定の許可リストも無い。有効にするには `enabled: true` と `paths` の両方が要る。
  `paths` を書いただけでは有効にならない。
- `enabled: true` で `paths` が無い、または空なら、起動時エラーにする（`files_paths_required`）。
  `paths` が書かれているなら `enabled` の値にかかわらず、各エントリを起動時に検証する。
- エントリは**絶対パスで、`path.Clean` の結果と一致するもの**だけを受ける。次はすべて起動時エラー。
  - `/` 単体
  - 相対パス
  - `path.Clean` と一致しないもの（末尾の `/`、`//`、`.`、`..` を含むもの）
  - `argSpec` を満たさないもの（空白、シェル用の記号、制御文字、512 バイト超。文字種は呼び出し元のパスと
    同じで、エントリだけ緩めることはしない）
  - `/proc`・`/sys`・`/dev` そのもの、およびその配下（プロセスのファイル記述子やカーネルの内部状態を
    指す magic link があり、SSM では root の視野に他プロセスの情報が出るため）
  - 同じエントリの重複（包含関係にある別エントリは許す）
- エントリ数の上限は **32**。上限を超えたら起動時エラー。
- エラーコードは `invalid_files_path_entry`（形の違反）、`forbidden_files_path_entry`（`/proc`・`/sys`・`/dev`）、
  `duplicate_files_path_entry`、`too_many_files_path_entries`。`field` は `operations.files.paths` で、
  エントリの値はメッセージに書かず、何番目かだけを書く。
- `paths` を `files` 以外の operation に書いたら起動時エラーにする（`unsupported_operation_field`。既存の
  `max_lines` / `max_since` と同じ扱い）。`files` に `max_lines` などを書いた場合も同じ。エラーの `allowed` には、
  その operation が受けるキー（`files` なら `enabled`・`sudo`・`paths`）を入れる。
  未知のキーを起動時エラーにする既存の仕組み（`yaml.DisallowUnknownField`）はそのまま使う。
- capability は `files: {SudoNever}`。config で sudo を選ぶ余地は無い。
- 許可リストは、`config.Operation` が検証済みの値（`vo.FilesAllowList`）として保持し、`max_since` と同じく、
  config を読んだ側（CLI / MCP）が `operation.Params` に入れて渡す。CLI と MCP は型名を書かずに
  `opCfg.FilesAllowList()` の値を運ぶだけである。`internal/vo` は `internal/config` を import しない。
- 経路の検査（後述）を緩める設定キーは作らない（緩める設定は issue #37 で扱う）。

### 呼び出し元のパス

`files` の入力は 1 つの `path` で、必須とする（CLI は `focal HOST files PATH`、MCP は `inspect_files` の `path` 引数）。
次をすべて満たすものだけを受ける。1 つでも欠けたら、リモートへ何も送らずに拒否する。

1. 絶対パスである。
2. `path.Clean` の結果と一致する。末尾のスラッシュ、`//`、`.`、`..` を含むものは拒否する。
   正規化して通すことはしない。呼び出し元の文字列と、照合する文字列と、リモートに渡す文字列を
   常に同じバイト列にするためである。
3. `argSpec` を満たす。文字は `A-Za-z0-9_@+=:,./-` だけで、空白は含められない。512 バイト以下。
4. 許可エントリのどれかと**一致する**か、`エントリ + "/"` を**前方に持つ**。
   `/var/log` を許可したとき、`/var/log` と `/var/log/nginx` は通り、`/var/log2` は通らない。

1〜3 に反すれば `invalid_path`、4 に反すれば `path_not_allowed`（`allowed` に設定済みのエントリを載せる）。
どちらも `kind` は `validation` で、executor を作る前に返る。

### 型: 照合を通ったことを表し、出どころはガードテストで押さえる

`internal/vo` に、照合を通ったときだけ作れる型と、その照合先の許可リスト型を置く。

```go
type FilesAllowList struct {               // config が起動時に作る
	entries [32]string
	n       int
}
func ParseFilesAllowList(entries []string) (FilesAllowList, *result.Error)
func (l FilesAllowList) Entries() []string // 複製を返す

type FilesPath struct{ s string }          // 照合を通ったパス。作れるのはこの関数だけ
func ParseFilesPath(raw string, list FilesAllowList) (FilesPath, *result.Error)
func (p FilesPath) String() string
func (p FilesPath) IsZero() bool
func (p FilesPath) Chain() []string        // "/", "/a", "/a/b", ..., p 自身（検査の引数列）
```

許可リストのエントリは配列で持つ。`FilesAllowList` の値をコピーすると、エントリもコピーされる。受け取った
スライスは構築時に複製し、`Entries()` も複製を返すので、返した値を書き換えても照合結果は変わらない
（テストで固定）。配列にしたことで `operation.Params` は比較可能なままになり、既存のテストの比較を
書き換えずに済んだ。

`internal/ssh` の factory（`FilesCommands`）は、パスの引数として `vo.FilesPath` だけを受ける。
`TestExportedFactoryParametersAreTypedValues` は `policy.Policy` か `vo.*` しか許さず、`internal/security_test.go`
も同じ条件を独立に検査している。どちらの許容型も広げない。

**型が保証するのは「許可リストとの照合を通った」ことだけである。** 次は、型では保証できない。

- その許可リストが管理者の config から来たこと
- その型の値が、ほかの operation（#29・#30）に流用されないこと

どちらも、後述のガードテストで押さえる（型の保証としては書かない）。型は operation ごとに別にする。
`FilesPath` と `FilesAllowList` は `files` 専用で、#29・#30 は**別の config キーと自分の型**を定義し、
`files` の型を受けない。型パラメータのタグで 1 つの型を共有する形は採らない。

**採らなかった形:**

- 素の「検証済みパス」型（絶対・Clean・`argSpec` だけを確かめた型）を factory が受ける形。照合が済んだことを
  型が表さないので、後続の operation が照合を忘れても、コンパイルもガードテストも通ってしまう。
- 「config だけが作れる鍵の型」を `FilesPath` の構築に要求する形。`vo` は `config` を import できず、
  Go には、別パッケージの 1 つだけに構築を許す言語機能が無い。強制できる具体形を示せないので、
  保証としては書かない。

残る穴を、次のとおり塞ぐ。

- **ゼロ値。** `vo.FilesPath{}` は `vo` の外からも作れる。factory は先頭で `IsZero()` を明示的に検査して
  拒否する。`argSpec` が空文字を拒否することには頼らない。
- **`vo` の外での偽造。** factory は `IsZero()` に加えて、`/` 単体でないこと・絶対であること・`path.Clean` の
  結果と一致すること・`Chain()` の末尾が自身であることを検証し直す（`unsafe` で偽造した値を食わせて、
  拒否されることをテストで固定）。
- **比較は `p.String()` で行う。** operation は生の入力文字列を保持せず、照合を通った `FilesPath` だけを
  持ち回る（`Files` 型のフィールドは `vo.FilesPath` 1 つだけであることをテストで固定）。
- **`UnmarshalText` / `UnmarshalJSON` などは実装しない。** フィールドは非公開のままで、外部からの
  書き換え手段を持たない。

**ガードテスト**（`go/ast` で。`go/types` は使わず、ファイルごとの import 名とパスの対応を解決した ast で
検査する）。どれも、違反する入力を食わせて落ちることを確かめるテストを付けた。

- (a) `vo.ParseFilesAllowList` への**参照**が、`internal/config` の外の非テストコードに無い。呼び出しに限らず、
  関数値として取る場合（`f := vo.ParseFilesAllowList`）も、import 名を変えた場合（`v ".../vo"`）も、`vo` の中で
  定義以外から参照する場合も含む。`vo` の dot import（`. ".../vo"`）は禁止する
  （`internal/files_guard_test.go`）。
- (b) `internal/config` の公開された関数・メソッドで、許可リスト型を返すものは、**引数を取らない**。
  呼び出し元の入力から許可リストを作れる公開ラッパーを置けないようにするためである。ポインタで返す形も
  検出する（`internal/config/guard_test.go`）。
- (c) 任意のバイト列・文字列・`io.Reader` から `Config` を作れる公開関数が無い。`internal/config` の公開関数で、
  戻り値が `Config`・`Operation`・`vo.FilesAllowList` に触れるものは `Load` と `LoadFrom` の 2 つに固定し、
  両方が `loadFrom` を通り、`loadFrom` がファイルの所有者と mode を確かめる `readConfigFile` を通ることを検査する。
  `LoadFrom` は `--config` で名指したパスを読む既存の入口で、`Load` と同じ権限の検査を通る。`Config` を返す
  公開メソッドは `Config` を受け手にするもの（`Config.Operation` の引きだけ）に限る（`internal/config/guard_test.go`）。
  (b) は直接の戻り値しか見ないので、`Config` を経由する間接の経路は (c) で塞ぐ。
- (d) `FilesAllowList{...}` と `FilesPath{...}` のフィールド付き複合リテラルが、それぞれの唯一のコンストラクタ
  （`ParseFilesAllowList` / `ParseFilesPath`）の本体にしか無い。両型への型変換、両型のポインタレシーバ、
  コンストラクタの外でのフィールドへの代入も禁止する。`vo` の公開関数・メソッドで両型を返すものの一覧を
  この 2 つに固定する（`internal/vo/files_guard_test.go`）。
- (e) 非テストコードでの `unsafe` の import を、`internal/` と `cmd/` の全体で禁止する。`reflect` は禁止しない。
  `reflect` だけでは非公開フィールドに値を書けないためである（`internal/files_guard_test.go`）。
- (f) `FilesPath`・`FilesAllowList`・`ParseFilesPath`・`ParseFilesAllowList` を名指せるファイルを、
  `internal/operation/files.go`・`internal/operation/registry.go`（`Params` と registry の行）・
  `internal/ssh/commands.go`・`internal/config`・`internal/vo` に限る。`vo` の外で両型の上に型の別名や
  定義型を作ることも禁止する（`internal/files_guard_test.go`）。

(a)〜(f) を合わせて、「許可リストは config が権限を確かめたファイルの YAML から作った値だけで、`files` の
operation と factory の外へは流れない」という前提を、型ではなくテストが守る。

### 経路の所有権の検査（transport を問わず常に実行する）

`ls` の前に、許可内の文字列であることとは別に、**対象への経路を root 以外が変えられないこと**を確かめる。
symlink の有無を `realpath` で確かめるだけでは、確認と使用のあいだの差し替え（コンテキストの 2）を
塞げない。差し替えるには、経路上のどれかのディレクトリへの書き込み権が要る。書き込み権を持つ者がいない
経路なら、確認のあとに差し替えられることがない。

検査は**リモートコマンド 1 回**で行う。

```
ls -d -n -b --time-style=full-iso <p1> <p2> ... <pn>
```

引数は `vo.FilesPath.Chain()` が返す、`/` から対象までのすべての接頭辞を、浅い順に並べたものである
（`/var/log/nginx` なら `/`、`/var`、`/var/log`、`/var/log/nginx`）。接頭辞は呼び出し元のパスの前方部分だけで、
リモートの出力から作られたものを含まない。argv は `lit("-d")`・`lit("-n")`・`lit("-b")`・
`lit("--time-style=full-iso")` と、`val(<接頭辞>)` の並びである。`literalSpec` の文字（`A-Za-z0-9_@+=:,./%-`）
に収まり、`argSpec` は `/` で始まる値を許す。先頭のハイフンを拒否するので、オプションに化けない。

`-d` は各引数自身を 1 行で出す（中身を展開しない）。`-n` は所有者とグループを数値で出す
（名前解決に依存しない）。1 つ目の引数（`/`）から最後（対象）まで、引数の最終要素を lstat するので、
symlink は種類が `l` になる。

**判定の順序。** 出力は次の 3 段で、段ごとに全体を見てから次の段へ進む。

1. **検査自体が成功している。** 終了コードが 0 で、`Truncated` でなく、stdout が改行で終わる。非 0 終了、
   `Truncated`、timeout、実行エラー（ssh の失敗・終了コード 255 を含む）は、すべて `path_check_failed`。
2. **全行を構文解析し、行と接頭辞が名前で 1 対 1 に対応する。** `ls` は引数を並べ替えて出力するので、行の順序
   と引数の順序を対応づけには使わない。各行の名前の欄を、期待する接頭辞と完全一致で突き合わせる。各接頭辞が
   ちょうど 1 回ずつ現れ、**余分な行が 1 つも無い**こと（行数が接頭辞の数と一致し、期待に無い名前・重複した
   名前が無い）を要求する。1 行でも読めなければ、あるいは対応が合わなければ `path_check_failed`。
3. **各接頭辞を浅い順に判定する。** 下の規則に当たった最初の接頭辞で拒否する（`path_not_root_controlled`）。

**行の読み方。**

- 0x20（半角空白）の連続を区切りとして、`mode` `links` `uid` `gid` `size` `date` `time` `offset` の 8 欄を取る。
  そのあと**ちょうど 1 つ**の空白を挟んで、名前の欄が行末まで続く。`strings.Fields` や `unicode.IsSpace` は
  使わない。`links`・`uid`・`gid` は 10 進の数字だけ。`uid` は左から 3 欄目に固定して読む。`size` は数字を
  要求しない（`BLOCK_SIZE=human` なら `4.0K` になる。下記）。
- **この検査に限り、`mode` 欄を厳密に読む。** 10 文字、または 10 文字のあとに末尾 1 文字。末尾の文字は SELinux の
  `.` か ACL の `+` だけで、それ以外なら読めない行とする。2〜10 文字目は位置ごとに読む: 所有者・グループ・その他の
  順に `r`/`-`、`w`/`-`、実行の位置（所有者とグループは `x s S -`、その他は `x t T -`）。setuid・setgid・sticky
  （`s` `S` `t` `T`）は受け付ける。`2755` の root 所有ディレクトリ（`/var/log/journal` など）を、理由の分からない
  拒否にしないためである。
- **名前の欄は、種類が `l` なら最初の ` -> ` の前、それ以外なら欄の全体**とする。接頭辞は空白を含まないので、
  最初の ` -> ` が区切りである。symlink の行の名前を正しく読むことで、途中や末尾の symlink は `not_directory`
  として返る（`path_check_failed` にはしない）。`l` の行に ` -> ` が無ければ読めない行とする。

**判定規則**（同じ接頭辞では上から順に 1 つだけ返す）。

1. 種類が `d` でない（symlink・ファイルなど）→ `not_directory`。対象自身にも適用する。
2. 対象より浅い接頭辞（祖先）の `uid` が `0` でない → `not_root_owned`。
3. 祖先の `mode` の末尾が `+`（ACL）→ `acl_present`。
4. 祖先のグループの `w`（6 文字目）かその他の `w`（9 文字目）が立っている → `group_or_other_writable`。
   所有グループが何であっても、gid 0 であっても、一律に拒否する。例外は設けない。
5. **対象自身**は、種類が `d` であること（symlink でない）だけを要求する。所有者と権限は問わない。
   アプリが所有するディレクトリ自身（`/var/log/app`）は一覧できる。その配下（`/var/log/app/archive`）は、
   祖先の `/var/log/app` が root 所有でないので拒否される。

リモートの出力は、**拒否の方向にしか使わない。** 許可（照合を通ったパスか）は、リモートに何も送る前に
`vo.FilesPath` が決めている。検査の出力は、その許可を取り消せるだけで、広げることも、argv に入れることも、
許可判定に使うこともない。検査の出力そのものは呼び出し元に返さない（`Part` の `stdout` / `stderr` を載せない）。
拒否したときに返すのは、Focal 自身の前方部分の文字列（`Chain()` で作ったもの）と、固定語の理由の分類だけである。
uid・gid・`mode` の値は返さない。

**`realpath` は使わず、検査 1 回に置き換える（往復は 2 回）。** 同値の論証:

- `ls -d` は各引数の最終要素を lstat する。`/` から対象までのすべての接頭辞を引数に並べるので、経路の
  どの要素が symlink でも、その接頭辞の行が種類 `l` になり、規則 1 で拒否される。
- すべての接頭辞が symlink でないディレクトリなら、パスは symlink を含まない。そのとき `realpath -e` の
  出力は入力と一致する（`realpath -e` の完全一致が確かめていたことと同値である）。存在しない要素があれば、
  `ls` が非 0 で終わり、判定の 1 段目で拒否される（`realpath -e` が失敗するのと同じ）。
- 検査はそれに加えて、経路上のすべてのディレクトリが root 所有で、グループとその他に書き込み権が無い
  ことを確かめる。

`realpath` を残すと往復が 3 回になるだけで、保証は増えない。

**確認と使用のあいだに残る差。** 検査と `ls` は別のリモートコマンドである。その間に経路を変えられるのは、
検査が通った時点で root だけになっている（root 以外は、祖先のどのディレクトリにも書き込めない）。
帰納法で言える。`/` は検査が root 所有で書き込み不可と確かめる。`/` の直下の要素を変えられるのは root だけ
なので、その要素の検査結果は、検査の間に変わらない。以下、同様に最後の要素まで続く。この性質は、
各 lstat が別々の時刻に実行されても成り立つ。

#### 具体例

root 以外が書けるディレクトリの配下は、所有者を直しても使えない。次のとおりになる。

| パス | 結果 | 理由 |
|---|---|---|
| `/var/log`（root:root、755） | 通る | 祖先は `/` と `/var`。対象自身は所有者を問わない |
| `/var/log/nginx` | 通る場合がある | `/var/log` が root 所有で、グループとその他に書き込み権が無いとき。rsyslog の入った Ubuntu では `/var/log` が `root:syslog` の 775 なので**拒否される** |
| `/var/log/app`（アプリの所有） | 通る | 対象自身は所有者を問わない |
| `/var/log/app/archive` | 拒否 | 祖先の `/var/log/app` が root 所有でない |
| `/etc/nginx` | 通る | `/`、`/etc` が root 所有で書き込み不可 |
| `/home/deploy/releases` | 拒否 | 祖先の `/home/deploy` が root 所有でない |
| `/tmp/x`、`/var/tmp/x`、`/dev/shm/x` | 拒否 | 祖先の `/tmp` などが、その他に書き込める |
| ACL が付いた祖先 | 拒否 | `mode` 欄の末尾が `+` |
| 祖先が root 以外のグループで 775 | 拒否 | グループが書き込める。所有グループが何であっても一律に拒否する |

**既知の制約: グループが書ける祖先は、所有グループにかかわらず拒否する。** 例外（gid 0 だけを許す、など）は
入れない。rsyslog の入った Ubuntu では、rsyslog の postinst が `chgrp syslog` と `chmod g+w` を実行するため、
`/var/log` が `root:syslog` の 775 になる（コンテナで実測）。この構成では `/var/log/nginx` や
`/var/log/<app>` は一覧できない。`syslog` グループの構成員が `/var/log` の中の名前を差し替えられるので、
`/var/log` を経路に含めると、差し替えを防げないからである。`/var/log` 自体は対象として一覧できる。
Amazon Linux 2023 と `debian:12` のイメージでは、`/var/log` は `root:root` の 755 である。
この制約を緩める設定キーは作らない。緩める設定は issue #37 で扱う。

### factory: 検査と一覧を 1 つの関数で 1 つのパスから作る

```go
func FilesCommands(p policy.Policy, dir vo.FilesPath) (check, list Command, err *result.Error)
```

検査と一覧の 2 つの `Command` を、1 つの `vo.FilesPath` から 1 つの関数で作る。検査した経路と一覧する
ディレクトリが、別の値になる余地を factory の形で無くすためである。`boundary_test.go` の規則（戻り値が
`Command` に触れる公開関数は `allowedCommandFactories` に載る、第 1 引数が `policy.Policy`、引数は
`policy.Policy` か `vo.*`）にそのまま収まり、規則は緩めていない。

一方、factory ごとに argv を 1 行ずつ持つ既存のテストの表（`internal/ssh/commands_test.go` の `factoryCases`、
argv と SSM の golden、`internal/security_test.go` の表）は「名前 1 つ = Command 1 つ」を前提にしていた。
次のとおり、規則を緩めずに 2 つの Command を載せた。

- `factoryCases` と両 golden は、Command ごとに `FilesCommands/check`・`FilesCommands/list` の行を持つ。
  網羅の検査（`TestFactoryCasesCoverTheCatalogue`）は、行の名前の `/` の前を factory 名として読み、
  `allowedCommandFactories` との集合の一致を見る。
- `internal/security_test.go` の表は factory 1 つに 1 行のまま、`FilesCommands` の行は検査の Command を
  検査し、`TestFilesCommandsArgvCarriesNoDangerousBytes` が両方の Command に同じバイトの規則を掛ける。

**限界。** 型は、operation が `FilesCommands` を 2 回、別のパスで呼ぶことまでは防がない。`Files` 型が持つのは
`vo.FilesPath` 1 つだけで（テストで固定）、Recorder のテストが検査の最後の引数と一覧の引数が同じであることを
固定している。

### ls の引数と出力の解釈

```
ls -A -b -g -o --time-style=full-iso --block-size=1 <path>
```

`-R` は付けない。再帰はしない。サブディレクトリを見たいときは、そのパスで `files` を呼び直す。
呼び直しのたびに、同じ照合と経路の検査が走る。`ls` は、検査が通った場合にだけ実行する。

| オプション | 理由 |
|---|---|
| `-A` | `-a` だと `.` と `..` が出る。`..` の行は**親ディレクトリのメタデータ**で、許可範囲の外にある。`-A` は両方とも出さない |
| `-b` | 名前の改行・タブ・制御文字・空白・`\` を `\n` `\t` `\NNN` `\ ` `\\` のようにエスケープする。行の偽造と列ずれを防ぐ要。symlink の区切りの ` -> ` も、名前の中のものは `\ ->\ ` になり、区切りと区別できる |
| `-g` `-o` | 所有者列（`-g` が消す）とグループ列（`-o` が消す）を出さない。利用者名やグループ名は要望に無く、返さない |
| `--time-style=full-iso` | ロケールと経過時間で形が変わる既定の日付表記を避け、`YYYY-MM-DD HH:MM:SS.nnnnnnnnn ±hhmm` の固定形にする |
| `--block-size=1` | リモートの `BLOCK_SIZE` / `LS_BLOCK_SIZE` 環境変数で size 列が黙ってキロバイト単位などに変わるのを防ぐ。明示した `--block-size=1` が環境変数に勝つことを e2e で確かめた（下記） |

`--quoting-style=escape` は足さない（`-b` と同じ効果で、argv が増えるだけ）。リモートの環境を固定するための
`env LC_ALL=C ls ...` も採らない。`program` の欄に、別のプログラムを起動する役のプログラムを置かない方針
（ADR 0002 の `programSpec`）による。そのため、ロケールと `SendEnv` の影響は、解釈の規則で吸収し、
README に書く。

実出力（AL2023 の coreutils 8.32。非特権のユーザーで、e2e の fixture を採取。ubuntu 24.04 の coreutils 9.4 も
同じ形で、時刻が UTC になるだけだった）:

```text
$ ls -A -b -g -o --time-style=full-iso --block-size=1 /srv/focal-e2e/files/allowed
total 49152
-rw-r--r--  1    1 2026-01-02 03:04:05.000000000 +0900 a\ b\n-rw-r--r--\ 1\ 0\ 2026-01-02\ 03:04:05.000000000\ +0900\ forged
drwxr-xr-x+ 1 4096 2026-01-02 03:04:05.000000000 +0900 acl
drwxr-xr-x  3 4096 2026-01-02 03:04:05.000000000 +0900 appowned
lrwxrwxrwx  1   11 2026-01-02 03:04:05.000000000 +0900 ends\ -> -> regular.txt
lrwxrwxrwx  1   10 2026-01-02 03:04:05.000000000 +0900 link-out -> ../outside
-rw-r--r--  1    4 2026-01-02 03:04:05.000000000 +0900 name\ with\ space\nand\ newline\ ->\ arrow
dr--r--r--  2 4096 2026-01-02 03:04:05.000000000 +0900 noexec
drwxr-sr-x  2 4096 2026-01-02 03:04:05.000000000 +0900 setgid
...
```

形は、先頭に集計行、以降は `mode links size date time.ns offset name`。`--block-size=1` では集計行もバイト数に
なる。size 列は右詰めの空白で揃う。`mode` の末尾に `+` が付く行があると、ほかの行は `mode` のあとが空白 2 つに
なる。ファイルや symlink 1 個を指定すると、集計行無しで、フルパスの 1 行になる
（`lrwxrwxrwx 1 28 2026-11-25 08:02:25.932625085 +0000 /srv/focal-e2e/files/allowed-link -> /srv/focal-e2e/files/allowed`）。

#### 解釈の規則（fail closed）

stdout は 1 行ずつ、次のとおり解釈する。**解釈できない行は捨て**、捨てた件数を `data.skipped_lines` に
入れる。捨てた行の内容は返さない。区切りは常に 0x20（半角空白）の連続だけである。`strings.Fields` と
`unicode.IsSpace` は使わない（NBSP や全角空白を区切りと誤認しないため）。

1. 出力が切れているとき（`Truncated` のとき、または `Truncated` の旗に関係なく stdout が改行で終わらないとき）は、
   最後の 1 行を捨ててから読む。途中で切れた行を 1 エントリとして解釈しないためである。この行は
   `skipped_lines` に数えない。
2. 先頭行は集計行でなければならない。ロケールによって先頭の語が変わる（日本語環境は `合計`）ので語は見ず、
   「0x20 で区切った 2 語で、2 語目が数字だけ」の形を要求する。これを満たさない出力は、終了コードにかかわらず
   一覧全体を失敗（`error.code = unexpected_listing`）にする。ディレクトリではなくファイルや symlink を
   指していた場合（集計行が無く、1 行だけ）も、ここで拒否される。集計行は `skipped_lines` に数えない。
   **集計行は完全性の根拠ではない。** 先頭にあるので、末尾の欠けは示さない。役割は、`ls` がディレクトリを
   見せられたことの確認だけである。
3. 以降の各行は、先頭から `mode` `links` `size` `date` `time` `offset` の 6 欄を取り、ちょうど 1 つの空白のあとの
   残りを名前の欄とする。
   - `size` と `links` は 10 進数の数字だけ。size は 20 桁以下で uint64 に収まること。デバイスファイルの `1, 3` や、
     `--block-size=1` が効かなかったときの `4.0K` のような欄は、ここで捨てられる。
   - `date time offset` は `YYYY-MM-DD HH:MM:SS.nnnnnnnnn ±hhmm` として厳密に読む。読めたら RFC 3339
     （秒精度、オフセットはコロン付き。UTC も `Z` ではなく `+00:00`）に**組み直して**返す。リモートの文字列は
     素通ししない。
   - **種類は `mode` の先頭 1 文字だけで決める**: `-` は `file`、`d` は `directory`、`l` は `symlink`、
     `c b p s D M n P ?` は `other`。それ以外の文字は捨てる。**この一覧では `mode` 欄の長さを検査しない。**
     SELinux の `.` や ACL の `+` が末尾に付く行を、10 文字ちょうどを要求して捨てないためである。
     （経路の検査だけは厳密に読む。上記。）
4. 名前の欄は `-b` のエスケープを理解する走査で読む。逆スラッシュは直後の 1 文字と対にして 1 単位とし、
   対になっていない（エスケープされていない）0x20 だけを区切りとみなす。末尾に対の無い逆スラッシュがあれば捨てる。
   - `-` / `d` / `other` の行は、名前の中に区切りの空白が出てはいけない。出たら捨てる。
   - `l` の行は、最初の区切りの空白のあとに `-> ` と空でない行き先が続く。**行き先は捨てる。** symlink の指す先は
     許可外の場所を示しうるので返さない。`-> ` が続かなければ捨てる。
   - 単純な「最初に出てくる ` -> ` で切る」実装は使わない。名前が `a ->` で終わる symlink（`a\ -> -> target`）を、
     `a\` で誤って切ってしまうためである。
5. 名前に、生の制御文字（0x00-0x1f、0x7f）か、**0x80 以上のバイトが 1 つでも**あれば、その行を捨てて
   `skipped_lines` に数える。`-b` は、C ロケールでは非 ASCII を `\303\251` のように 8 進でエスケープするが、
   UTF-8 ロケールでは印字できる文字をそのまま出す。リモートのロケールは、OpenSSH の `SendEnv` / `AcceptEnv`
   や、ホスト側の既定で決まる。Focal の側でロケールを固定できない（上記）ので、非 ASCII の名前は一律に捨て、
   向きを揃える（双方向制御文字や NBSP を使った表示の偽装も同じ扱いで除く）。
   名前は **`-b` のエスケープを復号せずそのまま**返す（`a\ b` は `a\ b`）。復号すると、改行などの制御文字を
   返却値に再び持ち込む。

#### `ls` の終了コードと結果の表

先頭の集計行は、`items` を返すすべての行で必須である。`status` と `exit_code` は合成しない（executor が返した
値のまま。`Truncated` なら `truncated`、非 0 なら `failed`）。`unexpected_listing` を付けたときに `status` が `ok`
なら、`failed` にする（終了コードは変えない。ADR 0010 の `package_not_installed` と同じ扱い）。

| 終了コード | `Truncated` | 結果 | `complete` |
|---|---|---|---|
| 0 | なし | 全行を解釈して `items` を返す | 改行で終わり、`skipped_lines` が 0 なら `true`、それ以外は `false` |
| 0 | あり（SSM の固定上限など） | 最後の行を捨てて返す | `false` |
| -1 | あり（SSH の出力上限で打ち切り） | 最後の行を捨てて返す | `false` |
| 1 | なし | 読めた行を返す。`?` 埋めの行は捨てて `skipped_lines` に数える。`status` は failed のまま | `false` |
| 1 | あり | 読めた行から最後の行も捨てて返す。`status` は truncated のまま | `false` |
| 2 以上 | | `items` も `skipped_lines` も返さない | `false` |
| -1 | なし | `items` も `skipped_lines` も返さない | `false` |
| 実行エラー・timeout | | `items` も `skipped_lines` も返さない。`error` は executor のもの | `false` |

**`complete` は「欠けを検出できなかった」ことを表す。** `true` は、終了コード 0・`Truncated` なし・改行で終わる・
`skipped_lines` が 0 の 4 つが揃ったときだけである。終了コード 0 でも、読めない行を捨てたなら `false` にする。
第三者が非 ASCII の名前やデバイスファイルを置けば、`ls` は 0 で終わったまま行を捨てさせられるので、それを
`complete=true` で覆わないためである。逆に、`true` でも、出力上限がちょうど行境界で切れて executor が
`Truncated` を立てなかった場合は欠けを検出できない（`Truncated` の判定は ADR 0007 と executor の契約に依る）。
呼び出し側は `status` ではなく `complete` で一覧の欠けを判断する（`status` は終了コード 1 で failed のまま
`items` が有効な場合や、SSM の打ち切りで truncated の場合がある）。MCP のツール説明にもそう書く。

終了コード 1 は、GNU ls が一部のエントリを stat できなかったとき（`readdir` のあとに消えた、など。
ログローテーション中の `/var/log` で自然に起きる）に返す。実出力（AL2023・ubuntu で同じ。読み取りの権限が
`r--` だけのディレクトリを、非特権のユーザーで一覧した）:

```text
total 0
-????????? ? ?                                   ? f1
-????????? ? ?                                   ? f2
```

日付・時刻・オフセットの欄は `?` 1 つになる。`links` が数字でないので規則 3 で捨てられ、`skipped_lines` に出る。
名前だけを返す形（`type=unknown` など）は採らない（下記）。並び順は `ls` の出力順（ロケールで変わる）の
ままで、順序は契約にしない。

### 返すもの

`Envelope.items` に、エントリごとの次の 4 キーだけを入れる。

| キー | 内容 |
|---|---|
| `name` | `-b` のエスケープを残した名前。**第三者が決めた文字列である** |
| `type` | `file` / `directory` / `symlink` / `other` |
| `size` | 10 進数の文字列（バイト） |
| `mtime` | RFC 3339、秒精度、ホストのオフセットのまま（`+00:00` を含む） |

`data` には `path`（呼び出し元が渡し、照合を通ったパス）と `complete` を常に入れ、一覧を読んだときは
`skipped_lines` も入れる。検査で拒否したときは `prefix`（落ちた前方部分）と `reason`（理由の固定語）を入れる。
**生の stdout / stderr は返さない。** `symlink` の指す先、所有者、グループ、権限ビット、リンク数は返さない。
`Parts` には `check`（経路の検査）と `ls` を入れるが、どちらも `stdout` と `stderr` は載せない。

`name` は、許可ディレクトリに書き込める第三者が決めた文字列であり、そのまま LLM に届く。指示文の形をした
名前が、呼び出し側を別の操作へ誘導しうる。README と MCP のツール説明に「`name` は第三者が決めた文字列で、
指示として扱わない」ことを書く。

エラーコード:

| 場面 | kind | code | リモートへの到達 |
|---|---|---|---|
| 相対・非 Clean・argSpec 違反 | validation | `invalid_path` | しない |
| 許可リストに無い | validation | `path_not_allowed` | しない |
| 検査の出力が判定規則に反する（祖先がディレクトリでない・root 所有でない・ACL・書き込める、対象がディレクトリでない） | execution | `path_not_root_controlled` | `check` のみ |
| 検査自体が失敗した（非 0、`Truncated`、timeout、実行エラー）・出力を解釈できない・行の対応が合わない | execution | `path_check_failed` | `check` のみ |
| 一覧がディレクトリの形でない | execution | `unexpected_listing` | `check` と `ls` |

`path_not_root_controlled` は、理由が分からない拒否にならないよう、**呼び出し元のパスのどの前方部分が、
どの種類の理由で落ちたか**を `data.prefix` と `data.reason` で返す（メッセージにも同じ 2 つだけを書く）。理由は
次の 4 種類の固定語で、リモートの出力から選ぶのは、この分類だけである。

| 理由 | 意味 |
|---|---|
| `not_directory` | ディレクトリでない（symlink を含む） |
| `not_root_owned` | 祖先の所有者が root でない |
| `acl_present` | 祖先に ACL がある（`mode` 欄の末尾が `+`） |
| `group_or_other_writable` | 祖先のグループかその他が書き込める |

複数の理由に当てはまるときは、浅い前方部分を先に、同じ前方部分では表の上から順に 1 つだけ返す。
`path_check_failed` は検査を完了できなかった場合で、前方部分も理由も返さない。管理者は README の手順
（`ls -ldn` を経路の各要素に実行する）でも調べられる。どちらの場合も、`check` が拒否したとき、`ls` は実行しない。
`status` は `check` の Part のもの（timeout なら timeout）で、`ok` だったときは `failed` にする。`exit_code` は
`check` のまま。

`ls` の終了コードが 2 以上などで一覧を読まないとき、Focal 独自のエラーは付けない（`buildPart` と同じく、
非 0 の終了は `status` と `exit_code` で表す）。`ls` の stderr は返さないので、理由は README の手順で調べる。

`files` は `inspect` には含めない。パスを指定する operation であり、引数なしで全体を調べる `inspect` の
対象ではない。

### SSM でも既定で使える。見える範囲が変わることを起動時と説明に書く

`files` は SSH でも SSM でも、同じように使える。経路の検査は SSM でも同じように実行する。SSM のときだけ
緩めることも、SSM のときだけ掛けることもしない。transport の判定は既存の `Config.TransportEnabled` のまま
変えない。

SSM Run Command は**常に root で動く**（ADR 0007）。SSH では `ls` が SSH ユーザーの権限で動き、見えるのはその
ユーザーが読める範囲に限られるが、SSM では root が見える範囲になる。同じ `paths` でも、SSM では許可範囲内の
0700 ディレクトリ（`/var/log/audit` など）の配下まで見える。これを次の 3 か所に書く。

- `focal serve` と `focal mcp` は、`execution.transports` に `ssm` があり `files` が有効なとき、起動時に 1 回だけ
  stderr に「SSM では files の一覧が root の権限で取られる」という事実を書く（`warnPlaintextBeyondLoopback` と
  同じ扱い）。単発の CLI には出さない（transport を選んだのはそのコマンドラインを書いた本人である）。
  エラーにはしない。消す設定キーは作らない。
- MCP の `inspect_files` のツール説明に、「SSH ではその SSH ユーザーが読める範囲、SSM では root が読める範囲
  （0700 の配下を含む）が一覧される」ことを書く。
- README に、同じことと、SSM を有効にする前に `paths` を見直すことを書く。

応答ごとに「root で一覧した」と示す形は採らない。operation に transport を渡すことになり、operation は
transport を知らないという ADR 0007 の分け方を崩すためである。

### 原則との関係: config が到達範囲を与える最初のキー

`operations.files.paths` は、config が operation の**到達範囲**（どこを見るか）を与える最初のキーである。
これまでの config は、operation を有効にするか、sudo をどこまで許すか、上限値をいくつにするかを決めるだけで、
operation が触る対象は決められなかった。

`CLAUDE.md` の「config は capability を**狭めることしかできず**」は、**権限昇格**（sudo モード）についての
文であり、本 ADR でも成り立つ。`files` の capability は `SudoNever` で、config は何も広げられない。
一方、`paths` は「どこを見てよいか」という範囲を管理者が**与える**キーで、この文の対象外である。
両者を次のように書き分ける（`CLAUDE.md`、`internal/config` のパッケージのドキュメントコメントも直した）。

> config は、コードが決めた外枠の中で、管理者が場所を与える。外枠はコードにあり、config からは外せない:
> メタデータ（名前・種類・サイズ・更新日時）だけを返す、再帰しない、SudoNever、ファイルの中身を読まない、
> 経路が root だけが変えられるものであることを毎回確かめる。

config が決めるのは「外枠の中のどの場所か」だけで、実行するプログラム・オプション・argv の形はコードの
ままである。`paths` の値も、呼び出し元のパスと同じ形の検証（絶対・Clean・`argSpec` の文字）を起動時に通し、
照合にしか使わない。argv に入るのは、照合を通った呼び出し元のパス（とその接頭辞）だけである。
config ファイルは、他のローカル利用者が書き換えられない権限であることを起動時に確かめており
（`internal/config`）、その前提のうえで管理者の入力として扱う。

## 検討した代替案

- **SSM では既定で拒否し、`operations.files.allow_ssm: true` を書いたときだけ使える opt-in にする**: SSH の
  感覚で書いた許可リストが、SSM で黙って root の視野に広がらないようにする案。採らなかった。理由
  （利用者の言葉）:「主な手段はsshだが、ssmでも同じように使えないと調査の妨げとなるので。」
  代わりに、起動時の警告・ツール説明・README で見える範囲の違いを書き、残余リスクに挙げる（下記）。
- **`realpath -e` の出力と呼び出し元のパスの完全一致で symlink を除く**: symlink は塞ぐが、確認と使用の
  あいだの差し替えを塞げない。書き込める者が差し替えを繰り返せば当たる。経路の所有権の検査は、symlink の
  除外を同値に含み、さらに差し替えそのものを不可能にするので、置き換える。
- **経路の所有権の検査を SSM のときだけ掛ける**: SSH でも、`ls` が SSH ユーザーの権限で、そのユーザーが
  元々読める範囲より広い範囲（許可の外の、そのユーザーに読める場所）を、許可リストの名目で一覧させられる。
  transport で保証が変わると、管理者が読む README も、テストも二重になる。不採用。
- **グループの書き込み権に例外を設ける（gid 0 だけ許す、など）**: gid 0 が「root だけ」を意味するとは限らない
  （`root` グループに入っている非 root の利用者や、補助グループに gid 0 を持つサービスがいる）。例外を暗黙に
  作ると、そこが差し替えの経路になる。緩めるなら、管理者が信頼するグループを明示する設定として #37 で扱う。
- **`setpriv` / `runuser` で SSM の権限を落として `ls` を動かす**: `program` の欄に、別のプログラムを
  起動する役のプログラムを置かない方針に反する。落とす先の uid を config に持たせる必要も生じる。不採用。
- **`find -printf` で構造化した出力を得る**: 区切りに制御できる書式が使えて、`ls` の出力の解釈より素直に
  見える。しかし `-printf` の書式文字列は `\0` `\n` のようにバックスラッシュを要し、現在の `literalSpec`
  （`A-Za-z0-9_@+=:,./%-`）に無い。広げれば ADR 0002 の境界を、全 operation に効く形で緩める。不採用。
- **検査にファイルシステムの種類の確認（`stat -f -c %T`）を足し、NFS などを拒否する**: ホストの外から書き換え
  られるファイルシステムを fail closed にできるが、許すべき種類の一覧を決める根拠が無く、往復も増える。
  残余リスクとして README と本 ADR に書く。
- **検査の argv にも `--block-size=1` を足す**: 検査は size 欄を読まない（数字も要求しない）ので、`BLOCK_SIZE` で
  size 欄が `4.0K` になっても判定は変わらない。桁区切りで size 欄が 2 語に割れるような変化は、名前の欄の
  完全一致が崩れて `path_check_failed` に落ちる（拒否の方向）。argv は増やさない。
- **解決後のパスが許可リスト配下かを Focal 側で判定する**: リモートが返した文字列を許可判定に使い、さらに
  その文字列を `ls` に渡さないと「判定したものと実行するもの」が一致しない。リモートが返す文字列は、
  symlink の行き先として第三者が決められる。リモートの出力は、拒否の方向にしか使わない（上記）。
- **素の「検証済みパス」型を factory が受ける / 鍵の型で構築を縛る**: 上述。不採用。
- **ガードテスト (b) に `go/types` を使い、許可リスト型を推移的に含む戻り値をすべて列挙する**: 依存と実行時間が
  増える。間接の経路（`Config` を経由するもの）は、`Config` を作れる公開関数を `Load` / `LoadFrom` に固定する
  (c) で塞ぐ。
- **検査と一覧を別々の factory にする**: 2 つの factory に別々の `FilesPath` を渡せてしまい、同じパスであることを
  operation の実装と Recorder のテストだけが守ることになる。1 つの関数にまとめる形が `boundary_test.go` の規則に
  収まるので、そちらを採った。
- **`env LC_ALL=C ls ...`**: ロケールを固定できるが、`program` の欄に別のプログラムを置く。不採用。
  解釈の規則で吸収する。
- **終了コード 1 の `?` 埋めの行を、名前だけ（`type=unknown`、`size` と `mtime` は空）で返す**: 消えかけていた
  エントリの名前が分かるが、4 キーの形と値の意味が行ごとに変わる。行は捨てて `skipped_lines` に数え、
  `complete=false` にする。
- **不完全の理由を固定語で返す（`incomplete_reason` など）**: 切断・stat の失敗・行の破棄を区別できるが、
  呼び出し側の次の行動（範囲を狭めて呼び直す）は変わらない。`status`・`truncated`・`skipped_lines` から
  読み取れる。足さない。
- **`ls -R`（再帰）**: 出力量が読めず、各サブディレクトリの経路を検査しきれない。1 ディレクトリずつ呼び直す。
- **`ls` の生の stdout も返す**: symlink の行き先、所有者、権限ビットなど、返さないと決めたものが全部入る。
- **許可エントリのパターン（glob・正規表現）**: 照合の規則が複雑になり、誤って広い範囲を許可しやすい。
  プレフィックス一致だけにする。
- **`paths` を書いたら自動で有効にする / 既定の許可リストを持つ**: どこを見せるかは管理者が明示的に
  決める判断であり、Focal が先回りして決めない。

## 影響

- `internal/ssh` に factory が 1 つ（`FilesCommands`）増え、2 つの `Command` を返す。両ガードテスト
  （`boundary_test.go` の `allowedCommandFactories`、`security_test.go`）に、現実的な入力つきで登録した。
  argv と SSM のゴールデンファイルに、検査（接頭辞が 4 つ・2 つの例）と一覧の行を足した。
  `literalSpec` / `argSpec` / `programSpec` は変えない。既存の factory の argv と golden の既存の行は変わらない。
- `internal/vo` に `FilesAllowList` / `FilesPath` を足した。`vo` から `config` への import は無い。
- `internal/policy/capability.go` に `files: {SudoNever}` の行、`internal/config/defaults.go` に無効の行、
  `internal/operation/registry.go` に registry の行と `Params.Path` / `Params.FilesAllowList` を足した
  （capability → registry → 両ガードテストの順。`registry_test.go` が drift を検出する）。
- `internal/config` に `paths` を足し、検証を入れた。パッケージのドキュメントコメントを、本 ADR の書き分けに
  合わせて直した。`CLAUDE.md` の該当文も直した。
- 既存のテストのうち、次のものは足した operation と引数の分だけ期待値を増やした: `internal/policy` の
  `allOperations`、`internal/config` の既定の operation の一覧、CLI の `TestOperationSpecs_MatchesFocalsOperations` と
  `TestOperationSpecs_AllBuild`（`Params` に `Path` と許可リスト）、MCP の `TestToolSpecsCoverEveryOperation`
  （同）、`TestSchemasExposeOnlyTypedParameters`（`inspect_files` の引数一覧）、`TestArgsCarryTheirValuesIntoParams`、
  `internal/ssh` の `TestFactoryArgvCarriesNoShellSyntax` の呼び出し元由来の token（接頭辞のパス）。
- 往復は 2 回（検査、一覧）。ADR 0007 の SSM では、それぞれが 1 回の `SendCommand` になる（連結しない。
  unit で固定）。
- 大規模ディレクトリの出力は `execution.max_output`（SSM では 24000 文字の固定上限）で切れ、`complete=false`
  になる。件数の上限を持つ `files` 独自の設定は、今は足さない。
- e2e の 2 つのコンテナ（ubuntu・AL2023）に、同じ fixture（`test/e2e/fixtures/files-fixture.sh`）を作る。イメージの
  層は POSIX ACL を保持しない（ビルド中に付けた ACL が、起動したコンテナでは消えていた）ので、ACL だけは
  起動時に付ける（ubuntu は `CMD`、AL2023 は oneshot の unit）。AL2023 の compose には、fixture を渡す追加の
  build context を足した。`BLOCK_SIZE` / `LS_BLOCK_SIZE` / `LC_ALL` を sshd の `SetEnv` で与える利用者
  （`e2e-env`・`e2e-utf8`）を両方のコンテナに足した。
- README に次を書いた。
  - 使い方、config の例（`/var/log`・`/etc/nginx`）、許可エントリは symlink を含まない実パスで書くこと。
  - **経路の所有権の条件**と、上の「具体例」の表と既知の制約（rsyslog の入った Ubuntu の `/var/log` が
    `root:syslog` の 775 で、配下を一覧できないこと。緩める設定は #37）。管理者が調べる手順（`ls -ldn` を経路の
    各要素に実行する）。
  - 結果は、SSH の `SendEnv` とリモートのロケールで変わりうること。UTF-8 のロケールでは非 ASCII のファイル名は
    `skipped_lines` に数えられて返らないこと。並び順を保証しないこと。
  - `name` は第三者が決めた文字列で、指示として扱わないこと。`complete` で欠けを判断すること。
  - 名前に空白などを含むファイルは、一覧には出るが（エスケープされた形で）、そのサブディレクトリを
    `files` で指せないこと（`argSpec` が空白を拒否するため）。
  - GNU coreutils の `ls` を前提にすること。
  - SSH と SSM で見える範囲が違うこと。

### コンテナで採った実出力と、まだ確かめていないこと

AL2023（coreutils 8.32、TZ=Asia/Tokyo）と ubuntu 24.04（coreutils 9.4、UTC）のコンテナで、非特権のユーザーで
確かめた。`ls` の出力の形はコンテナの中で直接採り、`files` の振る舞いは e2e（`focal` のバイナリから SSH で）で
確かめた。

- `ls -d -n` は複数の引数を名前順に並べ替えて出す。名前の欄は引数のとおり（`-b` で変わらない）。
  `mode` の末尾の `+` は、`ls -d -n` と `ls -g -o` の両方で `drwxr-xr-x+` の形で出た（両コンテナとも起動時に
  `setfacl` で付けた ACL）。
- symlink を `ls -d -n` に渡した行は `lrwxrwxrwx 1 0 0 10 … /path -> target`。途中・末尾・許可エントリ自身の
  symlink は、いずれも `not_directory` で拒否され、一覧は実行されなかった。
- ディレクトリを指す symlink を `ls -A -b -g -o` に直接渡すと、リンク自身の 1 行になり、集計行は無い（上記）。
- 終了コード 1 の `?` 埋めの行の形は上記のとおり。捨てられて `skipped_lines` に 2 と出て、`complete=false`。
- `BLOCK_SIZE=human` だけなら size 列は `4.0K`、集計行は `total 20K` になる（コンテナの中で直接）。`--block-size=1` を
  明示すると、`BLOCK_SIZE=human LS_BLOCK_SIZE=human` でも size 列はバイトのまま変わらなかった（コンテナの中で直接、
  および e2e で sshd の `SetEnv` を与えた利用者と与えない利用者の一覧が同じであること）。
- 名前の中の `\` は `\\`、タブは `\t`、改行は `\n`、空白は `\ ` になる。`a ->` で終わる symlink は `a\ -> -> target`。
- `LC_ALL=C` では非 ASCII の名前が `caf\303\251` のようにエスケープされ、`LC_ALL=C.UTF-8` ではそのまま出る。
  **両コンテナとも、`SetEnv` を与えない SSH セッションの既定が UTF-8 のロケールだった**ので、既定の構成では
  非 ASCII の名前は返らず、`skipped_lines` に数えられる。
- 照合に通らないパス（`..`・末尾の `/`・`//`・`.`・相対・許可外・名前だけ前方一致する兄弟）は、`validation` の
  エラーで拒否された（executor を作る前の段階）。

まだ確かめていないこと:

- `mode` 欄の末尾の `.`（SELinux）。unit の fixture は想定の形である。
- 日本語などのロケールで集計行の語が翻訳された形（`合計 12`）。unit の fixture は想定の形である。
- BusyBox など `--time-style` / `--block-size` の無い `ls` での失敗の仕方（非 0 で終わり、検査で拒否される見込み）。
- SSM の実機での打ち切りの形。unit では executor の `Truncated` の契約に依っている。

### 残余リスク

- **root による差し替え。** 経路の検査は、root 以外が経路を変えられないことを確かめる。root は、検査と
  `ls` のあいだに経路を差し替えられる。root が敵対的なら、`files` の保証の外にある。
- **uid 0 以外で root と同じことができる主体。** ホストの `/var/log` などを bind mount した特権コンテナ
  （user namespace を使わないコンテナの root はホストの uid 0 である）や、`CAP_DAC_OVERRIDE`・`CAP_FOWNER` を
  持つ非 root のサービスは、検査が通った経路でも差し替えられる。「root だけが変えられる」の「root」には、
  これらの主体が含まれる。コードでは検出できない。こうした主体がいるホストでは、その配下を `paths` に書かない
  （README に書いた）。
- **ホストの外から書き換えられるファイルシステム。** 祖先が NFS・CIFS・EFS・virtiofs・9p などの上にあると、
  クライアント側の `ls -n` が uid 0・755 と表示しても、サーバーや他のクライアントの管理者がこのホストの外から
  rename できる。NFSv4 や CIFS の ACL は `+` として出ないことがあり、idmap の設定によっては表示される uid と
  実際の所有者がずれる。検出しない。
- **mount。** root が作った bind mount・volume・autofs などが、許可ディレクトリの配下にあれば、それを
  またいだ先の実体が一覧される。mount は symlink ではないので検出しない。mount を作れるのは root で、
  上の root の項に含まれる。
- **検査が見落とす権限の仕組み。** 検査が読むのは、所有者と `mode` 欄（と、その末尾の `+`）だけである。
  FUSE や overlayfs の独自の権限判定、POSIX ACL 以外の ACL（`ls` が `+` を出さない実装）、別のマウント名前空間で
  同じファイルシステムを書き込み可能に見せているプロセスは、`mode` 欄から読み取れず、見落としうる。
- **検査の出力が本物の `ls` から来ているという前提。** SSH では、コマンドは SSH ユーザーのログインシェルで
  解釈される。そのユーザーの環境（`PATH` やシェルの初期化ファイル）を書き換えられる者は、`ls` を偽の出力を返す
  ものに差し替え、検査を通らせうる。この者はすでにその SSH ユーザーとして任意のコマンドを実行できる。
  SSM では、root の環境を書き換えられる者に当たる。
- **許可範囲の名前は見える。** 許可したディレクトリの配下は、どの深さでも、名前・サイズ・更新日時が呼び出し元
  （エージェント）に見える。ファイル名自体が機密を含む場所は許可しない。名前は第三者が決めた文字列で、
  prompt injection の経路になりうる（README・MCP のツール説明に書いた）。
- **ロケールと `SendEnv` への依存。** 結果は、リモートのロケールで変わりうる。解釈できない行は捨てられて
  件数に出る。誤って余分な行を作るより、欠けて件数に出る方を選んでいる。
- **SSM では、管理者が SSH の感覚で書いた `paths` が root の視野で一覧される。** SSH では見えない 0700 の
  ディレクトリの配下の名前も見える。起動時の警告・ツール説明・README に書くが、拒否はしない。

## テスト

unit テストは先に書き、期待どおりの理由で失敗するのを確かめてから実装した。SSM の送信の分け方の unit テストと
e2e は、実装のあとに書いて確かめた。

- `internal/vo/files_test.go`: 絶対・Clean・`argSpec` の各違反、`/` 単体、`/proc`・`/sys`・`/dev` とその配下、
  重複、33 個目、許可リストとの照合（エントリと一致、`entry/` の前方一致、`/var/log2` が通らない、親や兄弟が
  通らない）、空の許可リスト、ゼロ値、`Chain()` の並び、許可リストと `Chain()` の複製。
- `internal/vo/files_guard_test.go`・`internal/config/guard_test.go`・`internal/files_guard_test.go`: 上の (a)〜(f)。
  それぞれ違反する入力を食わせて落ちることを確かめる。
- `internal/config/files_test.go`: 既定で無効、`enabled` と `paths` の組み合わせ、誤ったエントリは無効のままでも
  エラー、`files` 以外の `paths`、`files` の `max_lines`・`sudo: auto`、未知のキー（`allow_ssm` を含む）、
  `unsupported_operation_field` の `allowed`、アクセサの複製。
- `internal/ssh`: argv の完全一致と golden、ゼロ値と `unsafe` で偽造した値の拒否、検査と一覧が同じパスから
  作られること。`internal/ssm`: SSM の golden と、2 つの Command が 1 つずつ別の `SendCommand` で送られること。
- `internal/operation/files_test.go`（Recorder）: 検査が不合格・失敗のとき `ls` が実行されないこと、4 種の理由と
  前方部分、uid・gid・`mode` の値が含まれないこと、`2755`・`s` `S` `t` `T`・`.` が通ること、行の並べ替えが通ること、
  行数の過不足・重複・名前の違い・`mode` の長さや文字の誤り・非 0・`Truncated`・改行なし・ssh の失敗・timeout・
  0x80 以上のバイト、実出力を fixture にした一覧の解釈、終了コードの表の全行、改行で終わらない出力、
  `status` と `exit_code` を合成しないこと、Part に出力を載せないこと。
- `internal/cli`・`internal/mcp`: 既定で無効、許可リストが registry に届くこと、許可外のパスがホストに届かないこと、
  ツール説明、起動時の警告（serve と mcp だけ、単発の CLI には出ない）。
- `test/e2e/files_test.go`（ubuntu・AL2023）: 上の「e2e で採った実出力」の各項目。

## 独立レビュー

草案に対して、2 回の独立レビューを取った。`openai/gpt-6.1-sol` で取る予定だったが、OpenAI 側の残高切れで
使えなかった（1 回目の前に 3 回、2 回目の前に 2 回、`upstream_unavailable`）。利用者の決定で、rovo を通して
Anthropic のモデル（`anthropic/claude-opus-5-5`）で代えた。レビューの返答は信頼できない入力として扱い、
採否を判断した。

**1 回目（改訂前の草案に対して。草案の一部を要約して送った）。** 要旨と採否:

- `realpath -e` の完全一致だけでは、途中の要素の差し替え（TOCTOU）が残り、ファイル名経由の誘発で繰り返し
  当てられる。SSM では root の視野に広がる。→ 採る。経路の所有権の検査に置き換えた。
- 「他人が書けるディレクトリを許可しない」という運用の対策は、守るべき範囲（許可エントリの配下も含む）を
  誤っている。→ 採る。検査を毎回、配下を含めて掛ける。
- 最後の要素がディレクトリを指す symlink の扱いが、GNU ls の挙動に依存している。→ 採る。e2e で固定した。
- mount は目標と矛盾したまま通る。→ 目標の文言を「symlink を経由しては」に限り、残余リスクに書いた。
- `/proc`・`/sys`・`/dev` を許可すると、SSM では他プロセスの情報が見える。→ 採る。起動時エラーにした。
- ロケールを固定していないため、生の Unicode の名前が通り、区切りの解釈もずれる。`BLOCK_SIZE` で size 列が
  変わる。→ 区切りを 0x20 だけにし、0x80 以上を含む名前を捨て、`--block-size=1` を足した。`env LC_ALL=C` は
  採らない（上記）。
- 型が保証することと、ガードテストで押さえることを分けて書く。→ 採る。

**2 回目（改訂 2 の全文に対して。末尾の草案用メモも含めた）。** 改訂 2 は SSM の opt-in を含んでいたが、
レビュー 2 に、opt-in を前提に経路の検査や残余リスクを評価した箇所は無かった。opt-in を外した本 ADR でも、
レビュー 2 の指摘はそのまま当てはまる。要旨と採否:

- 1-1 uid 0 以外で root と同じことができる主体（特権コンテナ、DAC を迂回するケーパビリティ）。→ 残余リスクに書いた。
- 1-2 ネットワーク・共有ファイルシステム。対策として `stat -f` の種類の確認が提案された。→ 残余リスクに書いた。
  種類の確認は採らない（代替案）。
- 1-3〜1-5 帰納法・`mode` 欄の読み・グループの書き込み権の一律拒否に穴は無い、gid 0 の例外は作らない方がよい。
  → 変更なし。`mode` 欄を位置で読むことをテストで固定した。
- 2-1 symlink の行が名前で突き合わず、`not_directory` ではなく `path_check_failed` になる。→ 採る。`l` の行は
  最初の ` -> ` の前を名前として突き合わせる。
- 2-2 検査の argv にも `--block-size=1` を足す。→ 採らない（代替案）。
- 2-3 判定の順序を明記し、余分な行・重複を上書きで吸収しない。→ 判定の 3 段として書き、テストで固定した。
- 2-4 検査の出力が本物の `ls` から来ている前提（返答はここで途中で切れていた）。→ 残余リスクに書いた。
- 3-1 終了コード 0 でも `skipped_lines > 0` なのに `complete=true` になる。→ 採る。`complete=false` にする。
- 3-2・3-3・3-5 `Truncated` への依存、`status` と `complete` の読み違い、集計行が完全性を保証しないこと。
  → 記述の補足だけにした（`complete` は「欠けを検出できなかった」意味であること、`complete` で判断すること、
  集計行は完全性の根拠でないこと）。`incomplete_reason` は足さない（代替案）。
- 3-4 終了コード 1 の `?` 埋めの行。名前だけ返す案が出た。→ 捨てて `skipped_lines` に数える（`complete=false`）。
- 4-1 (b) は `Config` を経由する間接の構築を見逃す。`go/types` を使う案が出た。→ `go/types` は使わず、
  `Config` を作れる公開関数を `Load` / `LoadFrom` に固定するガードテスト (c) を足した。
- 4-2 (a) は `vo` の中の複合リテラルを見ない。→ 採る。ガードテスト (d)。
- 4-3 検査と一覧に別々の `FilesPath` を渡せる。→ 採る。`FilesCommands` 1 つで 2 つの Command を作る。
- 4-4 `unsafe` と `reflect` での偽造。→ 非テストコードの `unsafe` を禁止し (e)、factory で形を検証し直す。
  `reflect` は禁止しない。
- 4-5 型を operation ごとに分ける約束が強制されていない。→ 採る。ガードテスト (f)。#29・#30 は別の config キーと
  自分の型を使う、と本 ADR に書いた。

## 関連

- issue #28
- issue #29・#30（ファイルの中身を読む operation）。許可照合の考え方と経路の検査を参考にできるが、
  #29・#30 は別の config キーと自分の型を定義し、本 ADR の `vo.FilesPath` / `vo.FilesAllowList` を受けない
  （ガードテスト (f) が名指しを拒否する）。本 ADR の残余リスクは引き継がない。
- issue #37: 経路の検査のうち、グループの書き込み権の扱いを緩める設定。本 ADR は緩める設定キーを作らない。
- [ADR 0002](0002-command-construction-boundary.md): Command の生成境界。本 ADR は変えない。
- [ADR 0006](0006-e2e-test-strategy.md): e2e のコンテナ。files の fixture を両方に足した。
- [ADR 0007](0007-ssm-transport.md): SSM は常に root で動く。経路の検査と、起動時の警告の前提。
- [ADR 0010](0010-package-operation.md): 試した分を `Parts` に残し、終了コードを合成しない扱い。
- [ADR 0011](0011-logs-filter-and-window.md): `Envelope.items` の導入。
