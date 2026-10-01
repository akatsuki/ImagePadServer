# Nico comment PBO → wGPU atlas 実行計画

2026-09-25。正本はrepository直下の [niconico-pbo-atlas-plan.md](../../../niconico-pbo-atlas-plan.md)。状態: T0/P1/P2/P3/A1/A2/A3/F1完了。PBOはsync既定、Atlasは選択可能だがSeparateを既定に維持。

順序: 基準計測 → PBO実装・採否確定 → アトラス実装・採否確定 → 実worker検証・後片付け。

wgpu 0.20.1を維持し、x264選択と通知付きCPUフォールバックを残す。PBOは同期readbackの方が速く、既定はsync。AtlasはGPU画素一致・順序・saved T0 parityを通し、選択可能な状態で残した。1080p30および720p/60fpsの僅差を複数sessionで追試したが、再現する時間短縮は確認できず、Separateを既定にした。実worker MP4+HLS、解像度/FPS回帰、H.264 single-slice、cleanupも完了。詳細は正本とtimeline acceptanceに記録。

小さくても再現する改善は採用対象とし、改善率・短縮秒数の足切りは設けない。僅差は追加測定で誤差との区別を確認する。
