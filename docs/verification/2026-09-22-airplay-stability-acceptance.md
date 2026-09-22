# AirPlay 不安定化修正の自動検証記録

実施日: 2026-09-22 (Asia/Tokyo)

## 対象

共有作業ツリーの既存未コミット変更を保持したまま、AirPlay の source-clock、VideoView、LL-HLS readiness、runtime compatibility 表示を修正した。commit、push、release、稼働中サービスの停止は行っていない。

## 実装確認

- source-clock receiver の capability manifest、必須 feature、実 EXE SHA-256 を起動準備時と source-clock 開始直前に検査するようにした。
- runtime preparation と source-clock compatibility を分離し、旧 runtime が解決できても source-clock 開始可能とは表示しないようにした。
- 稼働中 VideoView 更新で session/generation を必須化し、欠落は invalid、旧世代・旧 revision は stale として分離した。
- VideoView の sidecar/control 更新を一意な一時ファイルと atomic replace にし、sidecar 書込み失敗で revision を先行更新しないようにした。
- control 公開後の settings 保存失敗を `persistenceState=failed` として保持し、同一 request の再送を revision 増加なしの保存再試行にした。
- publisher candidate/retry の新 ready path へ VideoView intent を準備し、candidate commit 後に controller を adopt するようにした。
- LL-HLS の未来の `PRELOAD-HINT` artifact は readiness の HTTP fetch 条件から外し、既に完成した init map と part を readiness proof にした。

## 自動検証

| 区分 | 結果 | 備考 |
|---|---|---|
| `go test ./internal/airplay -count=1` | PASS | 専用 repo-local `GOCACHE` で実行 |
| VideoView / runtime compatibility focused tests | PASS | identity、永続化失敗、世代引継ぎ、明示 receiver preflight |
| `go test ./internal/server ./internal/obsrtmp` の対象 AirPlay/HLS tests | PASS | API/UI、LL-HLS、direct readiness |
| `go test ./internal/airplay ./internal/server -count=1` | AirPlay PASS / Server FAIL | Server 全体は 305 秒後に FFmpeg 不在、AppData ACL、既存 temp/process cleanup、非AirPlay media tests で失敗。総合 PASS とは扱わない |
| `Invoke-Pester -Path scripts/tests/test-package-airplay-source-clock.Tests.ps1` | PASS 9/9 | package provenance/feature checks |
| `Invoke-Pester -Path scripts/tests/test-build-uxplay-source-clock.Tests.ps1` | PASS 11/11 | patch order/build guard checks |
| 初回 `go test ./internal/airplay ./internal/obsrtmp` baseline | AirPlay PASS / obsrtmp環境依存FAIL | FFmpeg download ACL と GPU readiness fixture timeout。今回修正対象の AirPlay package には該当しない |

Go 実行時には既定の `C:\Users\masah\AppData\Roaming\go\telemetry\local\upload.token` への Access Denied が毎回表示された。ユーザー領域の ACL や telemetry 設定は変更していない。テストは専用 repo-local cache を使って完走した。

## 未実施・受け渡し

- 8080/8082 の待受は実施時点で `NOT_RUNNING`。現用サービスへの停止・入替えは行っていない。
- 現在インストールされている `single-slice-release-v1.8.0` の capability manifest は `video-bootstrap-reconnect` を含まないため、修正後の source-clock preflight は意図どおり incompatible と判定する。既存 release input の hash/URL は変更していない。
- GStreamer MSVC x64 development root が `C:\gstreamer\1.0\msvc_x86_64` に存在しないため、native bridge の再ビルドと実 runtime ZIP 候補の生成は未実施。旧 runtime を候補として扱っていない。
- 実 iPhone、ブラウザ実描画、VRChat、長時間連続試験は未実施。実機テストはユーザー側で新しい一致 runtime candidate を用いて行う。

この記録の PASS は fixture/unit/API/script 層の証拠であり、実機・配布候補の production-ready 判定ではない。
