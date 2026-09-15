# AirPlay の配布ライセンスと対応ソース

## 配布方式

Windows向けはexe一本に実行用ランタイム、ライセンス本文、ソース取得案内、再ビルド手順を収録する。
通常利用ではソースの取得・ビルドは不要。完全な対応ソースとパッチは、同じGitHubリリースの `ImagePadServer-AirPlay-Sources.zip` として別配布する。
GitHubが自動生成する本体の Source code ZIP だけでは第三者ランタイムの対応ソースを満たさない。
利用者は「設定 → アプリ情報 → AirPlayのライセンスと対応ソース」から通知をオフラインで閲覧でき、同じ画面から対応ソースZIPを無償で取得できる。
エンドポイントは `/licenses/airplay`。ローカルのファイル取得はexe内の許可されたものだけをストリームで返す。
`source-distribution.json` に版固定の取得URL・SHA-256・サイズを保持する。ソースZIPはexeと同じ期間公開・保持する。

ImagePadServerの独自コードはMIT。第三者コードをMITとして一括表示しない。
UxPlayはGPL-3.0-or-later、x264はGPL-2.0-or-later。GStreamerと依存ライブラリには個別の条件がある。
各条件・著作権表示は同梱の `license-manifest.json` と各ライセンス本文を参照する。

## 2026-09-16 の対応範囲

- 配布ランタイムID: `single-slice-release-v1.7.1`（元: `single-slice-licenses-20260916`）
- 102コンポーネント、616本の実行ファイル/DLL、337件のソース・ライセンス関連ファイルを照合。
- GStreamer 1.28.6と依存ライブラリ92件のソースを公式CerberoレシピのSHA-256で検証。
- RustのCargo.lockに対応する1,516件のregistryクレートと5つのgit依存リポジトリを収録。ビルド・テスト用依存を含む。
- GCC 14.2.0 / MinGW 12のソースと、実際のツールチェーンのビルド元 `0a9fb4e3` のレシピも収録。
- 受信側DLLはMSYS2 UCRT64の公式バイナリパッケージとのSHA-256一致を確認し、同版の完全ソースパッケージを収録。
- UxPlayは固定コミット `437f37514257d9cb513ac7fbdee743b4da85852e` にパッチ0001〜0011を適用した完全ソースを収録。
  元のビルド出力と同梱exeのハッシュ、保存済みのリンク対象を照合した。
  後から追加された0012〜0014を、この受信バイナリのビルド入力として扱わない。
- ブリッジの対応ソースとビルドスクリプト、H.264の1フレーム1スライス検査を収録。

動作済みのランタイムから全616本のバイナリを維持し、ソースの配布場所と通知を変更する。
別環境でのビット単位の再現ビルドは未検証。以下のゲートは収録物の整合性検査であり、法的判断やコーデック特許の許諾を代替しない。

## 再パッケージと検査

ソース集には `source-manifest.json`、`SOURCE-OFFER.md`、そこから参照する実ファイルを用意する。
ソース集の取得元・ハッシュは `sources/gst-sources.json`、`sources/msys-sources.json`、
`sources/toolchain-sources.json`、`sources/rust-crates.json` に含む。
完全なビルド方法は同梱の `sources/BUILD.md` を参照する。

```powershell
python scripts/package-airplay-license-bundle.py `
  --runtime <動作確認済みのruntime.zip> `
  --bundle <ソース集のディレクトリ> `
  --output <新しい出力ディレクトリ> `
  --runtime-set-id <新しいID>

python scripts/verify-airplay-h264-archive.py <出力runtime.zip>
python scripts/verify-airplay-license-archive.py <出力runtime.zip>

# 完全版から実行用と対応ソースを分離する（既存ファイルは上書きしない）
python scripts/split-airplay-release.py --runtime <完全版runtime.zip> `
  --output <新しい出力ディレクトリ> --tag v1.7.1 --repository akatsuki/ImagePadServer
python scripts/verify-airplay-license-archive.py <実行用ZIP> <対応ソースZIP>
```

`scripts/build-release.sh` も、指定された実際のZIPに両方のゲートを適用してからexeへ埋め込む。
`go build -tags airplay_runtime_embedded` を直接使う場合も、先にこの2つの検査を必ず通す。
`source-manifest.json` は両方のZIPで一致させる。ソース本体の欠落や改変は別ZIPでも検査する。
実行用ZIPはbootstrapで、対応ソースZIPは実行用ZIP内のdescriptorでサイズ・ハッシュを固定する。

## GitHub Actions とリリース

1. 実際の2つのZIPを検査し、生成した `release-inputs.json` を `release/airplay-inputs.json` に反映する。バイナリZIPはGitへ登録しない。
2. 本体Version、入力manifestのreleaseTag、作成するタグを一致させる。
3. 同じタグのドラフトリリースへ実行用ZIPと対応ソースZIPをアップロードする。
4. コードをPRで取り込み、対応コミットへタグをpushする。
5. Releaseワークフローがドラフトから固定ハッシュの2ZIPを取得・検査する。Windowsの空データ領域への埋め込みランタイム展開、通知とソース取得リンクをテストする。
6. 全プラットフォームのビルドとWindows検査が成功し、対応ソースを再取得して検査できた場合だけドラフトを公開する。失敗時はドラフトのままとする。

ローカルで同じ入力を検査・埋め込み準備する場合は `python scripts/prepare-airplay-release.py --inputs <2ZIPのディレクトリ>` を使う。
`scripts/build-release.sh` に渡す場合は従来のruntime pinsに加え `AIRPLAY_RUNTIME_SOURCES_PATH` を指定する。
タグの更新・移動で失敗をごまかさない。公開済みリリースの入力を変更する場合は新しい版とタグを作る。

ゲートは次の場合に失敗する。

- 対応ソース本体、ライセンス本文、ビルド手順の欠落、空ファイル、ハッシュ不一致。
- 新しいDLL/実行ファイルの追加、バイナリの変更、対応コンポーネントの未登録。
- GStreamerの実際の版一覧と対応ソースの版の不一致。
- 未解決のソース対応付け、ZIP内の重複名、パス逸脱。
- 対応ソースZIPの欠落、サイズ・ハッシュの不一致、2ZIP間の版や通知の不一致、同一リリース以外へのソースリンク。

依存の更新時は実際の配布バイナリ・レシピ・完全ソースの対応付けを更新する。
ホームページのリンクやパッチだけを完全ソースの代わりに登録しない。
このゲートとH.264互換性ゲートを回避・無効化して配布しない。

## テスト

```powershell
python -m unittest discover -s scripts/tests -p 'test_*airplay*archive.py'
go test ./internal/airplay ./internal/server -run 'TestRuntimeLicense|TestAirPlayLicense' -count=1
go test -tags airplay_runtime_embedded ./internal/airplay ./internal/server `
  -run 'TestPreparePinnedAirPlayRuntimeFromEmbeddedArchive|TestRuntimeLicense|TestAirPlayLicense' -count=1
```

埋め込み版の検査では新規データディレクトリへの展開と、ランタイムをインストールする前の
ライセンス画面・対応ソースへのアクセスを確認する。起動中の配信を停止する必要はない。
