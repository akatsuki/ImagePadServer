# X動画化 v1.9.0 release

User authorization: 2026-10-03「バージョン切ってリリースして」。v1.8.6が現在のLatest。新機能の追加をv1.9.0で公開する。ユーザーのrelease指定で統合/commit/push/tag/公開を承認済み。共有dirty checkoutと稼働中の配信/設定は保護する。

Ruling: origin/mainのisolated managed worktreeへ対象変更だけを移す。v1.8.6で追加済みの埋め込みX取得ESMとintent routingは維持し、開発用コードの古い分岐へ戻さない。AirPlayは既存の検証済みruntime/source pairのバイナリを維持し、同じ新タグへ対応ソースと通知を再梱包する。release gateを省略しない。

### T1: Scoped integration
- **depends_on**: []
- **location**: internal/xpost*, internal/voicevoxruntime, server/app/settings/library integration
- **description**: 現在のX動画実装をlatest mainへ移し、embedded fetcherとimage/music/video intentとの互換を統合する。無関係なNico/RTSP/GPU変更は含めない。
- **validation**: 差分所有範囲、既存intent/embedded-fetcher回帰、関連Go/Node/Rust検査、native worker.
- **ownership**: main
- **status**: completed

### T2: Distribution scope review and plan review
- **depends_on**: []
- **location**: build-release.sh, release.yml, xpost helper/fetcher resolution, Cargo.lock
- **description**: read-onlyでX helper/Node取得/VOICEVOXの配布漏れ、license notice、architecture/lookup、計画の依存を確認。
- **validation**: 実ファイル/行を根拠に具体的な不足を返す。state/secrets/外部APIには触れず、変更/commitをしない。
- **ownership**: bounded read-only worker
- **status**: completed

### T3: Release packaging and version
- **depends_on**: [T1,T2]
- **location**: internal/about, Windows resources, release inputs, build/release workflow scripts
- **description**: v1.9.0/FileVersion/resourceを一致させ、Windows/Linux/macOSのX helperとnoticeを配布へ含める。AirPlay二ZIPをv1.9.0へ再梱包し、actual ZIPを両gateで検査。
- **validation**: 配布zip構造/hash/CPU helper version/native protocol、埋め込みfetcher、全Go suite、Python archive gates、release version identity.
- **ownership**: main; production/stateful/external actions are main-only
- **status**: pending

### T4: Commit and publish draft inputs
- **depends_on**: [T3]
- **location**: scoped Git branch, GitHub PR/draft
- **description**: 対象のみcommit/push、PRを作成してchatへattach、レビュー/CI成功後に統合。タグに対応するdraftへAirPlay pairをupload。
- **validation**: commit diff、checks、head SHA、tag/Version/input一致、draft assets/hash.
- **ownership**: main
- **status**: pending

### T5: Tag, release CI and published artifact verification
- **depends_on**: [T4]
- **location**: annotated tag v1.9.0, Release workflow, published assets
- **description**: 対応コミットへtagをpush。release workflowの全gateと公開を確認し、公開ZIPを再取得して同梱X helper/embedded fetcher/license/runtimeを検査。
- **validation**: workflow success、isDraft=false/tag/target/assets、exact ZIP hash/構造、Windows executable file version/native worker、source pair再検査。VRChat実機を未検証と区別。
- **ownership**: main
- **status**: pending

Wave 1: main=T1, worker=T2+plan review。Wave 2: main=T3。Wave 3: main=T4。Wave 4: main=T5。

Reason_not_testable: version/tag/publication自体はTDDで機能差を作らない。実packaging、metadata/hash、fresh unit/native/CIとpublished artifactを検査する。

Release integration evidence (2026-10-03): Latest main intent routing and embedded react-tweet MIT banner preserved. Go targeted X/image/routing and Nico JS harness pass; Node 12 pass; Rust 10 pass; actual GPU UV crop pass; Windows ZIP architecture/license/source shader hash pass; file/product version v1.9.0. AirPlay exact v1.8.6 pair was hash-checked, unified, repacked for v1.9.0 without binary changes; both archive gates and 20 archive regressions pass. Full Windows media suite hit existing UDP port collision in AirPlay bridge E2E; isolated same test passed. Cross-platform fresh CI and published artifact qualification are required before publication.
