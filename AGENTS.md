# RTSP 映像互換性の変更規則

AirPlayの配布物・依存ライブラリ・ライセンスを変更する場合は `docs/AIRPLAY_DISTRIBUTION_LICENSES.md` を読み、
配布する実際のZIPに `scripts/verify-airplay-license-archive.py` を必ず適用する。
実行用ランタイム・通知・版固定のソース取得案内・ビルド手順をexe内に保持する。
完全な対応ソースは同じリリースの別ZIPとして公開し、実行用ZIPと対応ソースZIPの組を検査する。
未解決項目を消して検査を通したことにしない。

RTSP、OBS、AirPlay、H.264エンコーダー、配布ランタイムを変更する前に
[docs/RTSP_H264_COMPATIBILITY_CONTRACT.md](docs/RTSP_H264_COMPATIBILITY_CONTRACT.md)
を必ず読むこと。この契約はプリセットや性能調整より優先する。

- 送出H.264は1フレーム1映像スライス。CPU数・解像度・縦横・再接続・フォールバックで破らない。
- `zerolatency`だけに依存しない。`sliced-threads=0:slices=1:slice-max-size=0:slice-max-mbs=0`を保持する。
- 回帰テストの削除・除外・期待値緩和で変更を通さない。実際の圧縮出力と配布物のハッシュを検査する。
- 未検証のGPU経路や別ランタイムを、既存の実機成功と同等に扱わない。
- ローカル単体テスト・AVProハーネスの成功をVRChat実機での成功と混同しない。
- 共有中の既存変更を保護し、明示指示なしにcommit/push/releaseや稼働中配信の停止を行わない。
