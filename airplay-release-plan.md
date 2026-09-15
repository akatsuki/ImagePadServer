# AirPlay軽量リリース計画

ユーザー承認済み: ランタイム内蔵exeと別の対応ソースZIP、GitHub Actions修正、push/pull、リリース公開。
旧来の「対応ソースをexeに内蔵」は今回の指示で変更する。稼働中の配信は継続する。

## T1: 軽量パッケージと取得画面
- depends_on: []
- location: scripts/verify-airplay-license-archive.py, scripts/split-airplay-release.py, internal/airplay/runtime_licenses.go, internal/server/airplay_licenses.go, docs/AIRPLAY_DISTRIBUTION_LICENSES.md
- description: 完全な旧ZIPを検証して実行用ZIPとソースZIPへ分割。同一バイナリ・版・ハッシュ・ソース対応を両ZIPで検査する。exe内のライセンス画面に当該リリースのソースZIPへのリンクを表示する。
- validation: ソース欠落・改変・別版は失敗。616バイナリ一致。新規データディレクトリへ展開成功。exeは約170MB。
- owner: main（関連する包装・表示の一体変更）

## T2: GitHub Actions
- depends_on: [T1]
- location: .github/workflows/release.yml, .github/workflows/ci.yml, scripts/build-release.sh, third_party/airplay-runtime/release-inputs.json
- description: リリース下書きに事前添付したランタイムZIPと対応ソースZIPを固定ハッシュで取得。macOSの既存ビルドとWindowsの埋め込み検査を実施。exeとソースを同じリリースに揃え、全ジョブ成功後に公開する。
- validation: YAML検査、既存Go/Pythonテスト、実際のGitHub Actions成功。失敗した実行では公開しない。
- owner: main（GitHub状態と公開操作を含む）

## T3: 同期と公開
- depends_on: [T2]
- location: GitHub akatsuki/ImagePadServer
- description: 既存mainから分離した作業領域で必要なAirPlay/RTSP変更を含めてcommit、push、PR、merge、pull。v1.7.0の入力を下書きへ添付後にタグをpush。完了まで確認する。
- validation: 公開タグとコミット一致、Actions成功、exeと対応ソースZIPの存在・サイズ・SHA-256一致。
- owner: main

補助レビューは読取専用。認証・push・公開・稼働プロセス操作はmainが行う。
現在の検証対象はこの作業領域。元の共有チェックアウトにある無関係なGPU等の作業は含めない。
