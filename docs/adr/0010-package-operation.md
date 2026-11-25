# 0010. package operation: rpm を先に試し、dpkg-query は終了コードだけで選ぶ

## ステータス

Accepted

## コンテキスト

「この package は入っているか、どの版か」は、障害調査の最初の問いの 1 つである。ホストが RPM 系か
dpkg 系かは Focal には分からず、利用者も毎回は知らない。Focal の `Command` は固定 argv であり
（[ADR 0002](0002-command-construction-boundary.md)）、`rpm -q NAME || dpkg-query -s NAME` のような
シェル式は組み立てられない。したがって、2 つの管理コマンドのどちらを使うかは operation 側が
別々の `Command` として順に実行して決めるしかない。

その選び方を、リモートの出力の文言（"command not found" や "is not installed" の文面）に頼って決めると、
ホストのロケールと、第三者が書ける出力の内容が Focal の次の動作を決めることになる。
`logs` が sudo の再実行を終了コードの表だけで決めているのと同じ理由で、これは避ける。

## 決定

### 実行の規則

1. `rpm -q NAME` を実行する。終了コードが 0 ならここで終わる。
2. 0 以外なら（127 に限らない）、続けて `dpkg-query -s NAME` を実行する。
   どちらを実行するかは終了コードだけで決め、stdout / stderr の文言は見ない。
3. ssh 自体の失敗（executor がエラーを返す、または終了コード 255）が出たら、その時点で打ち切る。
   rpm が失敗したなら dpkg-query は実行しない。
4. 試したコマンドはすべて `Envelope.Parts` に入れる（Part 名は `rpm` / `dpkg`）。
   Envelope の `status` / `exit_code` / `error` は、答えを出した方の Part から採る。
   既存の `aggregate`（最悪の Part を採る）は使わない。dpkg 系ホストでは rpm の失敗が通常の経路であり、
   それを全体の失敗に数えると、入っている package まで failed と報告してしまう。
5. `data` には必ず `manager`（`rpm` / `dpkg`）と `installed`（`"true"` / `"false"`）を入れる。ただし ssh 自体の失敗（規則 3）と、パッケージ管理が見つからないとき（規則 7）は `data` を付けない。どちらも入っているかどうかを確かめられていないためである。
   `version` は取れたときだけ入れる。rpm は stdout の空行を除いた行がちょうど 1 行のとき、dpkg は `Version:` 行で、
   256 バイトを超える行は昇格させない（stdout には残る）。
   複数の版が入っている package（rpm -q が版ごとに 1 行を返し、終了コード 0 のもの）では `version` を入れない。
   rpm の出力順は DB の順序次第で、そのうちの 1 版を「その版」と言い切れないためである（kernel のような
   installonly の package では普通に起きる）。`installed=true` と `manager=rpm` は付け、全版は `rpm` Part の
   stdout に残る。
6. 入っていないときは failed とし、終了コードは答えた Part のものにする。`manager` は最後に答えた方
   （終了コード 127 でない方）である。dpkg だけが答えたなら `dpkg`、dpkg-query が無く rpm だけが
   答えたなら `rpm`、両方が答えたなら `dpkg` になる。
7. rpm と dpkg-query の両方が 127 のときは、`data` を付けず、`exit_code` 127 で
   `error.code = package_manager_not_found`（kind は execution）の failed とする。

### dpkg は `-s` を使い、Status 行で判定する

`dpkg-query -W NAME` は、削除済みで設定ファイルだけが残る package（`rc` 状態、`deinstall ok config-files`）に
対しても、入っている package と同じ終了コード 0 と同じ「名前 TAB 版」の行を返す。実際に Ubuntu 24.04 で
確かめ、区別できなかった。`dpkg-query -s NAME` は同じ状態で終了コード 0 を返すが、`Status:` 行が
`deinstall ok config-files` になる。そこで `-s` を使い、`Status:` の 3 語目が `installed` のときだけ
installed とする（`install ok installed` と `hold ok installed`）。0 を返しても installed でないときは、
`installed=false`、status failed とし、`error`（kind は execution、code は `package_not_installed`）を付ける。
終了コードは合成しない。top-level の `exit_code` も `dpkg` Part も、dpkg-query が実際に返した 0 のままにする。
理由は、`failed` の定義が「非 0 終了、または `error` に記録された実行時の失敗」であり、`error` で表せば
終了コードを偽らずに定義に合うからである。1 のような値を作ると、top-level と Part の終了コードが食い違い、
どのコマンドも返していない値を利用者が読むことになる。`error` のメッセージにリモートの出力は含めない。

`Status:` と `Version:` は行頭にあるものだけを欄として読む。`Description:` の継続行は先頭が空白で始まるため、
package の作者が説明文に書いた "Status: install ok installed" が判定に混ざることはない。
format 文字列（`--queryformat` / `-f`）は使わない。Focal が書くのは固定のオプションだけである。

### 入力と権限

- 入力は新しい value object `vo.PackageName` で受ける。文字は `A-Za-z0-9._+-`、先頭は英数字、長さは 1〜128。
  `+`（`libstdc++6`）を許す点と、`:`（dpkg では arch 修飾）と `@` を許さない点で `ServiceName` とは別の型にした。
  ワイルドカード・arch 修飾・版の指定は文法に含まれない。
- argv 上は `lit("-q")` / `lit("-s")` の後ろに `val(name)` が 1 つ付くだけで、`literalSpec` / `argSpec` は広げていない。
- capability は `{SudoNever}`。config は有効 / 無効だけを切り替えられ、既定は有効（読み取り専用）。
- `inspect` には含めない。package は名前を指定する operation であり、引数なしで全体を調べる `inspect` の対象ではない。

## 検討した代替案

- **`which rpm` などで管理コマンドを先に探す**: remote command が 1 つ増え、「見つかった」という出力の解釈が要る。
  終了コードだけで次を決める方針に反する。不採用。
- **rpm が 127 のときだけ dpkg-query を試す**: rpm が入っていても 127 以外で失敗する環境
  （権限や rpm データベースの不具合など）では、dpkg 側へ進めず「入っていない」と答えてしまう。
  節約できるのは実行 1 回で、規則も複雑になる。0 以外ならすべて試す方を選ぶ。
- **`dpkg-query -W`**: 上記のとおり `rc` 状態を区別できない。不採用。
- **`aggregate` で最悪の Part を採る**: dpkg 系ホストの成功が failed になる。不採用。

## 影響

- `internal/ssh` の factory が 2 つ（`RPMQueryCommand` / `DpkgStatusCommand`）増えた。ガードテスト
  （`boundary_test.go` / `security_test.go`）と、argv / SSM のゴールデンファイルに登録している。
- SSM transport でも同じ 2 コマンドが順に実行される。1 行のシェル文字列に連結はしない。
- 通信はホストによって 1 回または 2 回になる。dpkg 系ホストでは必ず rpm の失敗が 1 回先に付く。
- 他のパッケージ管理（apk・pacman・zypper の単体など）は扱わない。追加するときは factory と本 ADR の規則を拡張する。
- 既知の制約: dpkg-query の終了コードが 0・127・255 以外のときは、すべて「入っていると確認できなかった」（`installed=false`）として扱う。ロックや DB の破損と未インストールは区別しない。
