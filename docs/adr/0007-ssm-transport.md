# 0007. AWS SSM Run Command を第二の transport として追加する

## ステータス

Accepted

## コンテキスト

Focal は built-in operation を `internal/ssh` の Executor（`ssh(1)` 経由）でのみ実行してきた。
OpenSSH に到達できない環境（SSH 鍵配布が難しい、22番ポートを開けたくない、既に AWS Systems Manager
（SSM）で EC2 を管理している等）では Focal を使えない。AWS SSM Run Command
（`AWS-RunShellScript` ドキュメント）は EC2 インスタンスに SSM Agent と適切な instance profile が
あれば、SSH 鍵もネットワーク到達性も不要にコマンドを実行できる。

Focal の設計原則は "Focal never accepts arbitrary remote commands." であり、`internal/ssh.Command`
（0002 で確立した、フィールド非公開・`newCommand` のみが構築できる型）がリモートへ渡る内容を決める
唯一の境界になっている。この境界を transport の追加によって迂回できてはならない。

## 決定

### 新しい transport を `internal/ssm.Executor` として追加し、`ssh.Executor` インターフェースを実装させる

`internal/ssm.Executor` は `ssh.Executor`（`Execute(ctx, vo.Target, ssh.Command) (ssh.Output, error)`）
をそのまま実装する。`ssh.Command` の生成経路（`internal/ssh` の exported factory のみ）・
`vo.Target` の validation・`operation`/`policy` 層は一切変更しない。新しく作る `internal/ssm` は
「同じ `ssh.Command` を別の方法でリモートへ渡す」だけの層であり、Focal が実行できるコマンドの集合を
一切広げない。

AWS SDK for Go v2 への依存は、この transport が使う3メソッド（`SendCommand` / `GetCommandInvocation` /
`CancelCommand`）だけに絞った非公開インターフェース `api` 経由に限定し、`*ssm.Client` を直接
持ち回らない。AWS の認証・リージョン解決は SDK 標準の `config.LoadDefaultConfig` チェーン
（環境変数・instance profile・`AWS_REGION`）のみに委ね、Focal 独自のプロファイル/リージョン設定は
追加しない。

### `AWS-RunShellScript` を使い、Custom Document は作らない

`AWS-RunShellScript` は AWS が提供する標準ドキュメントで、`commands` パラメータに渡した文字列を
リモートのログインシェルで実行する。Focal 専用の Document を新たに設計・配布する選択肢もあったが、
運用者に追加の IAM 権限（`ssm:CreateDocument` 等）とデプロイ手順を強いるだけで、Focal 自身が
渡すコマンドを縛る境界の強さは変わらない（後述のとおり、境界は Document ではなく `ssh.Command` と
このパッケージのクォート処理にある）ため不採用。

### SSH もリモート側はログインシェルを経由しており、SSM との差は「誰が1行を組み立てるか」に限られる

`internal/ssh.OpenSSH.Execute` は argv を `ssh(1)` に渡し、`ssh(1)` はリモート側のログインシェルへ
argv を1つの文字列として送る（OpenSSH の仕様。リモート側で `sh -c` 相当の解釈が行われる）。したがって
「SSH はシェルを介さない」という理解は正確ではなく、正しくは「Focal 自身のプロセスとリモートの間に
Focal が書いたシェル文字列が存在しない」という意味でしかない。

SSM Run Command との実質的な違いは、Focal が `commands` パラメータとして**1行のシェル文字列を
自分で組み立てて**渡す点にある。この1行は `internal/ssm/command.go` の `commandLine` 関数
1箇所でのみ組み立てられ、`ssh.Command.Program()` / `Args()` が返す、すでに `argSpec` /
`literalSpec` / `programSpec`（0002 で確立した文字種制約）を通過した値だけから構成される。
各トークンは POSIX 単一引用符で完全にエスケープされ（`'` は `'\''` に変換する標準の POSIX
single-quote escape）、この二重の防御（文字種制約 + 完全なクォート処理）により、リモートシェルが
1トークンを2つ以上の argv 要素や別のコマンドとして解釈する余地をなくしている。

### 対象は EC2 インスタンスのみ（`i-` プレフィックス）

`vo.Target` の validation は transport に依存しないが、`internal/ssm` はその文字列を第二層として
再検証し、`^i-[0-9a-f]{8,17}$` に一致しないものを拒否する。ハイブリッドアクティベーションによる
managed instance（`mi-` プレフィックス）は明示的に拒否する。Focal の脅威モデルは「1つの IAM ポリシーが
及ぶ EC2 フリート」であり、`mi-` を受け付けると運用者が `ssm:SendCommand` のリソース条件をレビューして
いないオンプレミス/他クラウドのホストまで対象を静かに広げてしまうため。`user@i-...` のように SSH 用の
ユーザー指定が付いた文字列も拒否する: `user@` は SSM にとって何の意味も持たないため、同じホスト文字列が
transport によって異なる意味を持つ余地を作らない。

### 権限昇格: SSM Run Command は常に root で実行されるため、`sudo -n` の再試行は行わない

AWS-RunShellScript が起動するプロセスは常に root 権限を持つ。したがって:

- `policy.SudoAlways` のときだけ `sudo -n` を前置する。これは `ssh.PrefixesSudo` が返す最初の試行の
  判断そのままで、transport をまたいでコマンドの権限表示が一貫する。
- `policy.SudoAuto` の「非零終了コードでの1回だけの再試行」は行わない。SSM 上では unprivileged な
  最初の試行が権限不足で失敗するという状況自体が起こらないため、再試行しても同じ結果が返るだけで
  意味がない。

`internal/ssm.Executor` のドキュメントコメントに、この transport でのコマンドは常に root で動くことを
明記する。

### ステータス写像は `Status` enum と `StatusDetails` を分けて扱う

`GetCommandInvocationOutput.Status`（`CommandInvocationStatus` enum: Pending/InProgress/Delayed は
待機、Success/Failed は完了して `ResponseCode` を `ExitCode` にする — 非零終了コードはエラーにしない
点は SSH と同じ、TimedOut は `ssh.TimedOut` で判定できる timeout エラー、Cancelled/Cancelling は
専用のキャンセルエラー）とは別に、より詳細な `StatusDetails` 文字列（`DeliveryTimedOut` /
`ExecutionTimedOut` / `Undeliverable` / `Terminated` 等）を確認し、専用のエラーコード・文言に反映する。
`GetCommandInvocation` 呼び出し自体が `InvocationDoesNotExist` エラーを返す場合（`SendCommand` 直後は
インスタンス側にまだ記録が伝播していないことがある）は一時的なものとして再試行し、`InvalidInstanceId`
は「SSM 管理下にない・停止中・region が違う・IAM 権限不足」のいずれかであることが分かる文言にする。

### 出力上限: SSM 固定上限とマーカー検知、`MaxOutput` の3つを Truncated 判定に使う

SSM は `StandardOutputContent` を24000文字、`StandardErrorContent` を8000文字までしか返さず、
切り捨てられた場合に末尾へ `---Output truncated---` を付けることがある（付かないこともある）。
`internal/ssm` はこのマーカーの suffix 検知に加え、各ストリームが上記の固定上限に達している場合、
および両ストリームの合計が設定された `MaxOutput` を超える場合のいずれでも `Truncated=true` とする。
S3 への出力保存（AWS 側で提供される、24000/8000文字を超える完全な出力を取得する機能）は本 ADR の
スコープ外とし、ROADMAP に将来対応として記載する。

### 設定ファイルでの明示的な opt-in（`execution.transports`）

`execution.transports`（既定 `[ssh]`）に `ssm` を追加しない限り、CLI の `--transport ssm` および
MCP の `transport: "ssm"` は起動時ではなく呼び出し時に拒否される。Agent が呼び出しごとに transport を
選べる設計上、config による明示的な許可を挟まない限り「AWS 認証情報さえあれば SSM 経由で何でも実行
できてしまう」という運用者の意図しない拡大が起こり得るため、この opt-in を必須にする。未知の値は
起動時エラーにする。

### CLI/MCP の公開面: `--transport` フラグと `transport` ツール引数

CLI は `--transport ssh|ssm`（既定 `ssh`）を追加する。HOST 位置引数の意味は変えず、`--transport ssm`
のときは EC2 インスタンス ID として解釈される。`--identity` / `--port` / `--user` は SSM に対応する
概念がないため、`--transport ssm` と同時に指定するとエラーにする。

MCP は各ツールの引数に `transport`（`ssh` | `ssm`、省略時 `ssh`）を追加する。`host` の意味は
transport によって変わる（ssh のときは `[user@]host`、ssm のときは EC2 インスタンス ID）ため、
ツールのスキーマ説明にその旨を明記する。`identity` / `user` は `transport: "ssm"` と同時に指定すると
エラーにする。

## 検討した代替案

- **transport を config ファイルのみで固定し、tool call / CLI 引数では選べないようにする**:
  ホストごとに transport が異なる運用（一部は SSH 到達可能、一部は SSM のみ）を config だけで
  表現しようとすると、host 名と transport のマッピング表を Focal が持つ必要が生じ、
  「Focal はホストの意味を知らない」という既存の単純さを壊す。呼び出し側（Agent、あるいは
  Agent を使うオペレーター）が呼び出しごとに正しい transport を選ぶ方が単純であり、
  config の `execution.transports` による opt-in ゲートで濫用を防げるため、tool call / CLI 引数での
  選択を採用した。
- **Custom SSM Document で Focal 独自のパラメータ形式を定義する**: 上記のとおり、境界の強さは
  Document の形ではなく `ssh.Command` と `commandLine` のクォート処理にあるため、複雑さが増える
  だけで採用しなかった。
- **SSM でも `SudoAuto` の再試行を SSH と揃えて実装する**: root 常時実行の下では意味を持たない
  再試行を「一貫性のため」だけに実装すると、同じコマンドを常に2回送るという無駄な API 呼び出しに
  なり、`SendCommand` のレート制限やコストにも影響する。SSH との差分をドキュメントコメントで
  明記する方を選び、不採用。

## 影響

- `internal/ssm` は `internal/ssh` の exported command factory 一覧（0002・0004 で確立した境界）を
  一切変更しない。SSM 対応のために `internal/ssh` へ手を入れる必要はなく、新しいパッケージを
  追加するだけで済む。
- `internal/security_test.go` に、`internal/ssm` の exported 関数が bare な `string` を引数に
  取らないこと、および `SendCommand` の `commands` パラメータが `commandLine(ssh.Command, bool)` の
  呼び出し結果のみから構成されることを go/ast で検査するテストを追加した
  （`TestSSMExportedFunctionsTakeNoRawString` / `TestSSMSendCommandBuildsCommandsFromCommandLineOnly`）。
  `internal/ssm/boundary_test.go` にも同種のテストを置き、0002 の boundary_test.go /
  security_test.go の二重チェックという設計を transport の追加でも踏襲した。
- `execution.transports` を設定ファイルに追加したことで、`internal/config` は新しい起動時
  バリデーション（未知の transport 名・重複・空配列を起動時エラーにする）を持つ。
- 0001・0004 が確立した「Focal は `~/.ssh/config` を解釈しない」という決定は SSH transport にのみ
  適用され、SSM transport はそもそも SSH 接続を行わないため無関係のまま残る。
