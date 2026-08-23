# 0004. Remote MCP でも `.ssh/config` の解釈を OpenSSH に完全委任する

## ステータス

Accepted

## コンテキスト

[ADR 0001](0001-delegate-ssh-config-to-openssh.md) は CLI（`focal HOST OPERATION`）が `~/.ssh/config` を
自前で解釈せず、接続確立の詳細を OpenSSH に完全委任すると決めている。`internal/mcp`（issue #14）で
Remote MCP server `focal serve` を実装するにあたり、この判断が Remote MCP という別の入口でも
そのまま成り立つか、それとも MCP 固有の接続先定義（例: サーバー側で `{"alias": "prod-web", "hostname":
"10.0.1.20", "identity": "..."}` のようなホスト定義テーブルを持つ）を別途設ける必要があるかを決める。

`focal serve` は CLI と同じ `internal/operation` / `internal/ssh` を共有し、`internal/mcp` が
`internal/cli`（`serveRunner`）に渡す `Runner` インターフェース越しに実際の SSH 実行を委ねる構成に
なっている（issue #14 の設計上の制約: 「MCP handler 自身には SSH の知識を持たせない」）。Agent が MCP
tool 呼び出しで渡せるのは `host` / `user` / `identity`（alias）という文字列であり、CLI が受け取る
`HOST` 引数・`-l`・`-i` と対応関係にある。

Remote MCP 側に独自のホスト定義（`HostName` や `ProxyJump` に相当する情報)を持たせる設計も検討したが、
次の理由で採らない。

- `focal serve` は「`focal serve` を起動したユーザーの環境」で動く前提であり、そのユーザーはすでに
  `~/.ssh/config` に踏み台・多段 `ProxyJump`・ホスト別の鍵選択などの運用ノウハウを蓄積している。CLI と
  同じ資産を MCP 経由でも変更なく使えることは、CLI/MCP が同じ Core を共有するという設計（issue #13/#14）
  の直接の帰結であり、ここで別の定義層を挟むと CLI と MCP で接続先の解決結果が食い違う経路が生まれる。
- ADR 0001 が退けた理由（OpenSSH の接続制御ロジックの重複実装、Focal 側のバグが新たな攻撃面になる）は
  呼び出し口が CLI か MCP かに関係しない。MCP 側にだけ別のホスト定義パーサーを持たせることは、
  ADR 0001 が一度退けた設計をこの入口だけで復活させることになる。
- Remote MCP はネットワーク越しに Agent から呼ばれるぶん、CLI よりも「Agent が到達できる情報」を絞る
  必要が大きい。MCP 側にホスト定義を持たせないことは、`{"host": "prod-web"}` という呼び出しが
  `HostName` の実体（内部 IP など）を Agent 側にもサーバー側にも露出させずに解決される、という
  副次的な利点も持つ。

## 決定

`focal serve` は `~/.ssh/config` / `known_hosts` / ssh-agent の解釈を一切持たず、ADR 0001 と同じく
OpenSSH（`focal serve` を起動したユーザーの環境）に完全委任する。MCP tool の `host` パラメータは
CLI の `HOST` 引数と同じ `vo.ParseTarget` を通り、`ssh(1)` にそのまま destination として渡される
（`HostName` / `ProxyJump` 等は OpenSSH が解決する）。`internal/mcp` はホスト定義のテーブルを持たず、
接続の実行は `internal/cli` の `serveRunner`（`Runner` 実装）が CLI と同じ `internal/ssh.OpenSSH` を
通じて行う。

Remote MCP に固有なのは「鍵をどう選ばせるか」だけである。Agent には鍵ファイルのパスを渡させず、
`focal serve --identity alias=path` で事前登録した alias 名だけを公開する（`internal/cli/identity.go`）。
これはホスト解決の委任先を変える判断ではなく、Agent に「サーバー上のファイルパスを探索・指定する
能力」を渡さないための、MCP という入口固有の追加境界である。

## 検討した代替案

- **MCP 専用のホスト定義テーブルを `focal serve` の起動オプションまたは設定ファイルに持たせる**:
  Agent が指定できる `host` を administrator が事前登録したエイリアスに絞れる利点はあるが、
  `~/.ssh/config` の資産をこの入口だけ別形式で再定義する必要が生じ、CLI と MCP の間で同じホスト名が
  異なる解決結果を持ちうるようになる。issue #14 の「host は allowlist にしない」という設計判断
  （「突然渡されたサーバーを安全に調査させられる」ことが Focal の価値）とも相容れない。不採用。
- **MCP 層が独自に SSH 接続を確立する（`internal/ssh` を経由しない）**: `internal/mcp` に
  SSH の知識を持たせることになり、issue #14 の設計上の制約「MCP handler 自身には SSH の知識を持たせ
  ない」に反する。CLI と MCP で2つの接続実装を保守するコストも生じる。不採用。

## 影響

- `focal serve` のホスト到達性・接続互換性は CLI と同じく OpenSSH クライアントのバージョンと
  `~/.ssh/config` に依存する。ADR 0001 の README 上の推奨（OpenSSH 10.1+）がそのまま適用される。
- `internal/mcp` パッケージが `internal/ssh` を import することはテスト（`internal/mcp` の import
  境界検査）で禁止され、SSH 実行は常に `Runner` インターフェースの実装（`internal/cli/serveRunner`）
  経由になる。
- 「Agent が選べる鍵は事前登録した alias のみ」という制約は `~/.ssh/config` の委任範囲を狭めるもの
  ではなく、`focal serve` の起動オプション（`--identity`）という別レイヤーで運用される。
