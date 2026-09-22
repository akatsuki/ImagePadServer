# ニコニココメント書き出し高速化 T6 受入れ報告

作成日: 2026-09-22  
判定: **T6完了・候補不採用**

## 結論

T2のtee実装、workerの共通Export入口、staging保護、CPU20% Jobの起動契約は検証済みである。T3のD3D11/NVENC常駐経路は、現環境にMSVCとNVENCヘッダがないため`unavailable`である。

T6の通常利用採用は行わない。GPU候補はT3で`unavailable`、tee候補は同じsource・snapshot・FFmpeg・Chromeを固定した昇格CPU20%実workerの3条件40ペア統計で全条件が採用閾値未達となった。したがって計画の「候補が両方非適格なら見送り理由のみ確定する」分岐に従い、既定separateを維持する。これはVRChat併用の通常利用PASSではなく、候補を採用しないというT6最終判断である。

現環境では候補ビルドだけでなく、直前に成功していた旧workerを同じsource・snapshot・FFmpeg・Chromeで再実行したrestricted条件でもbrowser frame 0前にDevTools接続が切れた。Chrome/EdgeのログはGPUプロセス終了コード`0xC0000022 (STATUS_ACCESS_DENIED)`を示す。昇格条件ではworkerを完走させられたが、これは通常利用の権限条件を証明しない。

## 実装対象

- `NicoOutputMode`: 空/separate/tee。空は従来動作。
- `ExportNicoCommented`: MP4とHLSの両方が完成・検証されるまで成功を返さない共通入口。
- tee時は専用cwdの固定basenameからMP4/HLSを生成し、完了後に出力stagingへ原子的に移動する。
- workerはtee時に二回目のHLS FFmpegを起動しない。
- `IMAGEPAD_NICO_OUTPUT_MODE=tee`だけでteeを選択し、既定値はseparate。
- GPU候補は製品経路へ接続していない。

## 実施済み検証

| 項目 | 結果 | 証拠 |
|---|---|---|
| Go video/worker契約・tee・native cwd | PASS | `go test` focused suite |
| Serverのtee opt-in伝播 | PASS | `TestHandleUploadURLNiconicoCommentsPublishesPreparedMedia` |
| Python worker stabilityハーネスのtee指定 | PASS | `test_worker_stability.py` 4 tests |
| T6受入れ評価器のfail-closed判定 | PASS | `test_t6_acceptance.py` 8 tests、実験スクリプト群82 tests |
| GPU隔離runnerの契約 | PASS | 7 tests。ただし実行結果は`unavailable` |
| baseline manifest | PASS | `build/nico-cpu20/t0-baseline-20260922a/baseline-manifest.json`、`valid=true` |
| 製品ビルド | PASS | `build/nico-cpu20/t2-tee-build-20260922a/ImagePadServer-nico-next.exe` |
| 低レベルtee MP4/HLS生成 | PASS | `TestEncodeNicoCommentedTeeProducesMP4AndHLSFromOneEncode` |
| 保存済みNPS3→native compositor→FFmpeg tee（CPU20%） | PASS | `build/nico-cpu20/t6-nps3-tee-probe-20260922b/runner-report.json`、`media-validation.json`。360 frames、MP4/HLS、PTS、decode、AU 1 slice、keyframe、音声差分を検証 |
| 昇格環境の実browser worker 1ペア | PASS（探索） | `build/nico-cpu20/t6-elevated-baseline-20260922a/stability-report.json`、`t6-elevated-candidate-20260922a/stability-report.json`。両方CPU20% Job verified、worker完走 |
| 固定6秒40ペアの統計 | 閾値未達 | `build/nico-cpu20/t6-statistics-20260922d.json`。中央値speedup +0.432%、CI下限 -0.338%、p95差CI上限 +2.299%、CPU候補/基準579.734/588.625秒 |
| high-density-10s 40ペア | 閾値未達 | `build/nico-cpu20/t6-statistics-20260922d.json`。候補中央値49.8590秒、基準49.6565秒、中央値speedup -0.500%、p95差CI上限+2.393%、CPU候補/基準3214.922/3196.672秒 |
| real-source-155s 40ペア | 閾値未達 | `build/nico-cpu20/t6-statistics-20260922d.json`。候補中央値9.0155秒、基準9.0310秒、中央値speedup -0.083%、CI下限-0.754%、p95差CI上限+0.172%、CPU候補/基準579.156/585.922秒 |

## T6実worker再現試験

CPU20%のWindows Job runner自体は各試験で`verified=true`だったが、worker成果物は生成されなかった。

| 試験 | 結果 | 主な理由 |
|---|---|---|
| 新ビルド + tee + browser | FAIL | frame 0、WebSocket切断 |
| 新ビルド + separate + browser | FAIL | frame 0、WebSocket切断 |
| 旧worker + separate + 同一source/snapshot/FFmpeg/Chrome | FAIL | frame 0、WebSocket切断 |
| 旧成功時と同じ入力の再実行 | FAIL | 現在のbrowser環境では再現せず |
| 新ビルド + tee + native-WARP + VRChat起動中 | FAIL | browser sprite captureのWebSocket切断後、compositorが`truncated scene` |
| 旧worker + separate + native-WARP + VRChat起動中 | FAIL | 同じbrowser入力切断の後段で`truncated scene` |
| 新ビルド + tee + native-WARP・CPU制限なし（診断） | FAIL | 同じbrowser入力切断。20% Jobだけが原因ではない |
| 保存済みNPS3・native compositor単体 + VRChat起動中 | PASS | 360 frames、0.953秒、exit 0。compositor単体は成立 |

主なartifact:

- `build/nico-cpu20/t2-real-worker-tee-20260922a/stability-report.json`
- `build/nico-cpu20/t2-real-worker-separate-20260922c/stability-report.json`
- `build/nico-cpu20/t6-baseline-exact-20260922a/stability-report.json`
- `build/nico-cpu20/t3-gpu-nvenc-20260922a/result.json`

新形式のT6評価器でも同じ結論になった。baseline/candidateは同一の
FFmpeg・source・snapshot・Chromeハッシュを持ち、CPU20% Jobの
`runner_verified=true`も記録されたが、双方ともworker成果物/event生成に失敗した。
VRChatの3区間証跡も未提出なので、評価結果は`blocked`であり速度改善率は算出していない。

その後、Chrome GPUプロセスの権限境界を切り分けるため、同じ入力・Chrome・FFmpeg・CPU20% Jobを昇格プロセスから再実行した。候補teeと旧separate baselineはそれぞれ6秒を完走し、MP4/HLSのPTS、decode、AU 1 slice、HLS keyframe、音声差分が通った。これはrestricted実行時のframe 0失敗が権限境界の影響を受けることを示すが、昇格実行は通常利用の採用証跡ではない。

固定6秒を40ペアまで再収集したところ、baseline中央値9.055秒、candidate中央値8.977秒、中央値speedup 0.432%だった。10,000回bootstrapは中央値speedup CI下限-0.338%、p95差CI上限+2.299%で、tee採用条件（5%または0.25秒、CI下限/上限条件）を満たさない。最初の40回収集が19回付近で止まったのはobserver全体timeoutの不足だったため、必要反復数に応じてtimeoutを延長する修正を入れ、修正後の40回は完走した。

high-density-10sも40ペアまで収集した。baseline中央値49.6565秒、candidate中央値49.8590秒、中央値speedup -0.500%、p95差CI上限+2.393%であり、candidateの総CPU時間も3214.922秒とbaselineの3196.672秒を上回った。したがって固定6秒と高密度10秒の両方でtee採用条件を満たさない。実ソース155秒も40ペアまで収集し、中央値speedupは-0.083%だった。現在の統計入力は`build/nico-cpu20/t6-statistics-input-20260922c.json`、出力は`build/nico-cpu20/t6-statistics-20260922d.json`である。

high-density-10sとreal-source-155sもbaseline/candidate各40回まで収集した。high-density-10sは候補49.8590秒・基準49.6565秒、中央値speedup -0.500%、CPU候補/基準3214.922/3196.672秒だった。real-source-155sは候補9.0155秒・基準9.0310秒、中央値speedup -0.083%、CPU候補/基準579.156/585.922秒だった。40ペア・10,000回bootstrapを満たしても、両条件ともtee採用閾値を満たさない。real-sourceは4646フレーム、high-densityは300フレームで、両方とも媒体検証を通過した。

- `build/nico-cpu20/t6-acceptance-baseline-20260922c/stability-report.json`
- `build/nico-cpu20/t6-acceptance-candidate-20260922c/stability-report.json`
- `build/nico-cpu20/t6-acceptance-evaluation-20260922e.json`
- `build/nico-cpu20/t6-acceptance-native-candidate-20260922d/stability-report.json`
- `build/nico-cpu20/t6-acceptance-native-baseline-20260922c/stability-report.json`
- `build/nico-cpu20/t6-acceptance-native-evaluation-20260922d.json`
- `build/nico-cpu20/t6-native-unlimited-diagnostic-20260922a/worker-0001/worker-result.json`
- `build/nico-cpu20/t6-native-fixture-diagnostic-20260922a.json`

native-WARP試験ではVRChatプロセス自体は観測され、保存済みNPS3を使うcompositor単体は完走した。
一方、実workerではbrowser sprite captureが入力生成段階で切断する。GPU使用率・VRAMも取得できたが、
VRChatのPerformance Statsログが見つからずframe-timeは未計測だった。したがって、
VRChat併用中のCPU20%制約を満たした採用判定には進めていない。

直接のbrowser testでも同じframe 0失敗を再現した。PNG transport、GPU無効、Dawn cache無効、旧headless、software renderer、GPU sandbox診断、Edge 153への切替、workspace内専用TEMPへのprofile移動では解消しなかった。sandboxを弱める修正は採用していない。

## 固定したバイナリ

| artifact | SHA-256 |
|---|---|
| candidate `ImagePadServer-nico-next.exe` | `05E5ABEFB1431DE897BE47DFAD89E97EE8124CA2A970178FEC1680B3D19CCEBE` |
| diagnostic candidate with native exit-code logging | `31A87D1A5EA999D4AAAC5DAEB007EFD52BC52266242D53ECBA8B9D32B5DAF497` |
| old worker `imagepadserver-worker-current.exe` | `F35ABF0ED68CA03A62D1B4D4964846B86E5DE2509720AC16293955C8404A1199` |
| integration FFmpeg | `72A489ECCD008C2EC2C0A5856C5C75BC3D8BBFA90166C4566865C246445E6AA3` |
| diagnostic FFmpeg 8.1.1 | `09948D4CDD0650DA6FF5A87577469F2A218DC2615AE379F8F734D24C49DE0F73` |

BrowserはChrome `153.0.8010.48`、Edge `153.0.4234.48`を使用した。

追加診断ではPlaywright Chromium `1228`、headless shell、GPU sandbox無効、software-inprocess、SwiftShader-inprocessも試した。
Chromium 1228でもGPU process終了またはCDP timeoutとなり、headless shellはGPU process起動失敗後にtimeoutした。
sandboxを弱める経路や未検証の別browserを既定経路へ昇格させていない。

## 未実施・採用条件

以下は実workerがbrowser rendererを完走し、成果物を生成できる環境でのみ再開する。

- 720p/1080p、16:9/4:3/1:1、24/29.97/30/60/VFR、音声なし・短音声、高密度・CAのmatrix。
- separate対teeの6秒探索、155秒定常、最終40ペア、中央値・p95・pair bootstrap。
- MP4/HLSのPTS、音声packet、decode、faststart、keyframe、AU 1 slice、容量差。
- VRChat起動中のbaseline→candidate→baseline、CPU約20%、Job CPU、GPU/VRAM、p95/p99 frame-time。
- 実プレイヤーでの映像・音声確認、cancel/re-run、30分連続、残存process確認。

受入れ評価器の採用条件は、baseline/candidate双方の完走、CPU20%検証、成果物/event成功、
入力・実行パラメータhash一致、全シナリオ・品質matrix、40ペア/条件・10,000回bootstrap、cancel/re-run、30分連続、
VRChatの`baseline-before`/`candidate`/`baseline-after`各60秒以上、各区間のp95/p99
frame-time、CPU20%記録、実プレイヤー映像・音声確認である。いずれかの証跡が欠けると
`blocked`となる。

最新の機械的受入れ評価は`build/nico-cpu20/t6-acceptance-real40-evaluation-20260922d.json`で`blocked`である。これは全gateを満たす通常利用PASSではなく、3条件すべての候補閾値未達と、非適格分岐により未実施としたVRChat/matrix/operational証跡が理由である。40ペア統計上、今回のtee実装は従来separate実装より高速化していないため、既定separateを維持する。
