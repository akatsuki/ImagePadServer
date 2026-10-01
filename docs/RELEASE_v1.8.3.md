# ImagePadServer v1.8.3

## 変更点

- Windowsのニコニコ動画コメント書き出しで、連続ジョブ向けのworkerとブラウザー再利用を既定で有効にしました。各ジョブのページと描画状態は作り直します。従来のfresh worker経路へ戻す場合は `IMAGEPAD_NICO_WORKER_SESSION=0` を設定してください。
- コメントのtimeline出力とNCT2 WGPUコンポジターを追加しました。対応環境でGPU経路を利用し、利用できない場合のCPU経路も残しています。
- GPUエンコーダーに加え、CPUのlibx264を選べるようにしました。実行時にCPUへ切り替えた場合は画面に通知します。
- AirPlayの映像表示とHLS切り替えをsource clock基準にし、入力断後の復帰と状態遷移を改善しました。

## 配布物

Windowsランタイム、ライセンス通知、版固定の取得案内をアプリへ収録し、対応ソースを同じリリースの `ImagePadServer-AirPlay-Sources.zip` として配布します。ランタイムとソースの組はSHA-256、ライセンス在庫、H.264の1フレーム1スライス検査を通過しています。

WGPUの実機確認はWindows / RTX 5070 Tiで行っています。ほかのGPUやOSではGPU経路の同等動作を確認していないため、CPUのlibx264を選べる状態を維持しています。
