# 0003. YAML ライブラリに goccy/go-yaml を採用する

## ステータス

Accepted

## コンテキスト

`internal/config`（issue #3）は `~/.config/focal/config.yaml` を読み込み、型付き `Config` struct へ
デコードする。Go の YAML ライブラリとしては `gopkg.in/yaml.v3` が事実上のデファクトだったが、
2026-08 時点の調査（`outputs/research-web.md` §2）で次が確認されている。

- `gopkg.in/yaml.v3` は複数の依存プロジェクト側 issue（`spf13/viper#1773`、`go-task/task#2171` 等）で
  「アーカイブ済み・unmaintained」と報告されている。
- 既知の脆弱性として `GHSA-hp87-p4gw-j4gq`（Unmarshal 時の panic による DoS）があり、
  `v3.0.0-20220521103104-8f96da9f5d5e` で修正済みだが、以降の積極的なメンテナンスは期待しづらい。
- 代替候補として `goccy/go-yaml` があり、アクティブにメンテされ、anchor/alias 対応・カスタム
  marshal/unmarshal 対応などの機能面でも優位（`cli/cli#10784` が移行事例として存在）。

config ファイル自体はローカルの信頼境界内（`~/.config/focal/config.yaml`、利用者自身が書く）にあるため
優先度は「高」ではなく「中」だが、YAML パーサ共通の懸念として anchor/alias 展開による資源枯渇
（billion laughs 相当の展開爆発）がある。ライブラリ選定だけでこれを防ぎきれるものではなく、
Focal 側の対策と組み合わせる前提で選定する。

## 決定

`gopkg.in/yaml.v3` ではなく **`goccy/go-yaml`** を採用する。理由:

- アクティブにメンテされており、`gopkg.in/yaml.v3` のようなアーカイブ済みリスクを避けられる。
- 機能面（anchor/alias 対応、カスタム marshal/unmarshal 対応）で同等以上であり、Config struct への
  デコードという用途で不足がない。
- `gopkg.in/yaml.v3` からの移行事例（`cli/cli#10784`）があり、API 互換性の観点で採用リスクが低い。

anchor/alias 展開による資源枯渇（メモリ・CPU の異常消費）への対策として、Focal は **decode 前の
ファイルサイズ上限**でこれに対処する。具体的には `config.yaml` の読み込み時にファイルサイズを
上限値（実装は 1MiB を基準値とする）でチェックし、上限超過時はデコードを試みずに起動時エラーとする。
この対策はライブラリ側の anchor/alias 展開ロジックそのものに依存しないため、`goccy/go-yaml` に
限らずどの YAML ライブラリを使う場合でも有効な防御である。

本判断は `plan.json` の `new_dependencies` において `preauthorized: true`（停止条件に該当しない）として
事前承認済みであり、実装者（s9-config）が改めて依存追加の停止条件判定を行う必要はない。

## 検討した代替案

- **`gopkg.in/yaml.v3` を継続採用する**: 既知の DoS 脆弱性は修正済みバージョンを使えば実害は小さく、
  デファクトとしての実績もある。しかし unmaintained 状態が既に複数プロジェクトから報告されており、
  将来の脆弱性報告に対する修正が期待しづらい。長期メンテ方針としてのリスクを理由に不採用。
- **`yaml/go-yaml`（YAML organization が引き継いだフォーク）を採用する**: `gopkg.in/yaml.v3` からの
  引き継ぎ先として存在するが、`goccy/go-yaml` と比較して移行実績・機能面の優位性の調査が
  `outputs/research-web.md` の時点で `goccy/go-yaml` ほど確認できておらず、今回は見送る。
  将来的にこちらへの再移行が有利と判明した場合は本 ADR を改訂する。
- **anchor/alias 展開の防御をライブラリのデコードオプション（存在する場合の再帰深度・展開数上限）に
  委ねる**: ライブラリ側のオプションが将来変更・削除される可能性があり、Focal 側で制御できない
  依存になる。decode 前のファイルサイズ上限という Focal 自身が保証できる対策を優先し、ライブラリ側の
  オプションは補助的な扱いに留める。不採用（併用は妨げない）。

## 影響

- `go.mod` の依存に `github.com/goccy/go-yaml` を追加する（`gopkg.in/yaml.v3` は追加しない）。
- `internal/config` の decode 経路には、YAML パース前にファイルサイズ上限チェックを置く
  （上限超過時は起動時エラー。issue #3 の「起動時バリデーション」の一部として扱う）。
- config ファイルの信頼境界（ローカル、利用者自身が書く）を前提にした判断であるため、将来 Focal が
  リモート由来の YAML を読み込む用途を追加する場合は、本 ADR の前提（優先度「中」）を見直す必要がある。
