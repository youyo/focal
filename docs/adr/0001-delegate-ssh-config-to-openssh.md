# 0001. `.ssh/config` の解釈を OpenSSH に完全委任する

## ステータス

Accepted

## コンテキスト

Focal は `internal/ssh` の Executor を通じて `ssh [-i ...] [-p ...] -- <target> -- <remote argv...>` を
起動し、リモートホストへ接続する（issue #7）。ホスト接続に関する設定 — `HostName`、`ProxyJump`、
`IdentityAgent`、`ControlMaster`、`known_hosts` の検証方法 — は SSH クライアントの責務領域であり、
利用者はすでに `~/.ssh/config` にこれらを運用ノウハウとして蓄積している。

Focal がこれらを自前で解釈・再実装する（例: `ProxyJump` 相当のホップを Go コードで組み立てる、
`known_hosts` の照合ロジックを持つ）選択肢もあったが、次の理由で採らない。

- OpenSSH が長年運用してきた接続制御・鍵管理・ホスト検証のロジックを重複実装すると、
  Focal 側のバグが接続の安全性を直接損なう新たな攻撃面になる。
- Focal の設計原則は "Focal never accepts arbitrary remote commands." であり、価値の中心は
  **リモートで何が実行されるか** を型で縛ることにある。接続経路の確立方法まで再発明する必要はない。
- 利用者が既存の `~/.ssh/config` 資産（踏み台・多段 ProxyJump・SSH agent 経由の鍵管理等）を
  変更なしにそのまま使えることは、導入コストを下げる直接的な利点になる。

一方で、SSH クライアントには `ProxyCommand` / `LocalCommand` / `Match exec` の `%h` / `%u` 展開を経由した
既知の OS コマンドインジェクションが存在する。

- **CVE-2023-51385**（OpenSSH < 9.6, CVSS 9.8）: `ProxyCommand` などの `%h` / `%u` 展開時に、
  ホスト名やユーザー名に含まれるシェルメタ文字（バッククォート等）がサニタイズされずシェルに渡り、
  任意コマンド実行に至る。
- **CVE-2025-61984**（OpenSSH client < 10.1）: ユーザー名に含まれる制御文字（改行等）が `ProxyCommand`
  展開に未サニタイズのまま挿入され、シェルが引数を早期終端して新たなコマンドを開始できる。

Focal は `.ssh/config` を解釈しないため、これら2件は Focal 自身のコードパスではなく、**利用者の
`~/.ssh/config` に `ProxyCommand %h` 等の展開が書かれている場合、Focal が渡す `target` 文字列
（Value Object 経由で strict validation 済み）の内容次第で OpenSSH 側の展開に間接的に影響しうる経路**
として存在する。Focal はこの経路に対し、`target`（ホスト名・ユーザー名相当）にシェルメタ文字・制御文字
（改行含む）を reject する strict validation（issue #4 の Value Object）と、`argv` 組み立て時の `--`
挿入（`ssh -- <target> -- <remote argv>`）を防御多層として実装する。OpenSSH 自体のバージョンは
Focal の制御外だが、README/受け入れ条件で OpenSSH 10.1+ を推奨する。

## 決定

Focal は `HostName` / `ProxyJump` / `IdentityAgent` / `ControlMaster` / `known_hosts` を一切解釈せず、
接続確立の詳細をすべて OpenSSH（利用者の `~/.ssh/config` および OpenSSH 自身のデフォルト動作）に委任する。
Focal は `-o ProxyCommand` / `-o RemoteCommand` / `-o LocalCommand` のような escape hatch を提供しない
（これらのオプションを Focal の CLI/MCP 入力から SSH コマンドラインへ渡す経路を持たない）。

Focal 側の責務は次の2点に限定する。

- `target`（ホスト名・ユーザー名相当）の Value Object による strict validation
  （シェルメタ文字・制御文字・改行の reject を含む。CVE-2023-51385 / CVE-2025-61984 の攻撃パターンを
  injection reject テストケースに含める）。
- `argv` 組み立て時に `target` の直前へ `--` を固定挿入し、`target` が `-` で始まる文字列であっても
  ssh 自身のオプションとして解釈されないようにする。

## 検討した代替案

- **Focal が `~/.ssh/config` を自前でパース・解釈する**: ホップ経路や鍵選択を Focal 側で制御できる
  利点はあるが、OpenSSH の接続制御ロジックの重複実装になり、実装バグが新たな攻撃面になる。利用者が
  すでに持つ `~/.ssh/config` 資産をそのまま使えなくなるコストも大きい。不採用。
- **`-o ProxyCommand` 等を Focal の設定ファイルで許可する**: 利用者の柔軟性は上がるが、
  「Focal never accepts arbitrary remote commands」という設計原則に反し、`ProxyCommand` 自体が
  任意コマンド実行の経路になる。不採用。
- **接続確立を独自の SSH ライブラリ（例: `golang.org/x/crypto/ssh`）で実装する**: OpenSSH バイナリへの
  依存を外せるが、利用者の `~/.ssh/config` との互換性を Focal 側で再実装する必要が生じ、上記の
  重複実装コストがそのまま残る。不採用。

## 影響

- Focal のホスト接続互換性は OpenSSH クライアントのバージョン・設定に依存する。README/受け入れ条件に
  OpenSSH 10.1+ を推奨として明記する。
- `target` の Value Object における strict validation（issue #4）と `argv` の `--` 固定挿入
  （issue #7 の golden argv test 対象）が、CVE-2023-51385 / CVE-2025-61984 に対する Focal 側の唯一の
  緩和策になる。この2点のテストカバレッジは security regression test として維持する。
- Focal は `-o ProxyCommand` / `-o RemoteCommand` / `-o LocalCommand` を将来にわたり公開面に含めない
  （追加する場合は本 ADR の見直しが必要）。
