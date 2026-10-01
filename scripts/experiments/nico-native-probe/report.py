"""Japanese result report built only from saved experimental evidence."""
import json,pathlib,statistics
ROOT=pathlib.Path(__file__).resolve().parents[3];BASE=ROOT/'build/nico-native-probe'
s=json.loads((BASE/'summary.json').read_text(encoding='utf8'))
med=lambda a:statistics.median(a)
lines=['# コメント画像描画の高速化検証（2026-09-17）','',
'本番コード・稼働アプリは変更していない。元の RGBA テクスチャを使い、WARP 基準、連続する同一画像のまとめ描画、透明余白の切り詰め、実 GPU を比較した。文字画像生成と転送は、実際の `WriteSpriteStream` と同じ処理順を使う別の隔離ブラウザで計測した。','',
'## 条件と解釈','',
'- Ryzen 7 7800X3D、RTX 5070 Ti、driver 610.47、MSVC /MT /O2。','- 実コメント REAL: 1080p60、6秒・360フレーム・15テクスチャ・15,738コマンド。','- 高密度 F1: 1080p60、10秒・600フレーム・3テクスチャ・21,420コマンド。','- 画素比較は全フレーム。ベース画像は旧マスク実験の **original RGBA reference**。A1/A2/A4 の減色画像は使用していない。','- 性能値は各条件ウォームアップ1回の後に順序を変えて5回。各回プロセスを作り直すため、デバイス作成・シェーダーのコンパイルを含む。','- PC上の別の負荷は停止していない。GPU使用率・GPUエンコーダー使用率の変動を各 config.json の gpu_before に保存。範囲の重なる小さな差を確定的な改善率と扱わない。','- 合成単独は fwrite だけを省略し、描画・CopyResource・Map を維持。','- エンコード込みは保存済み実動画のデコード、1080p60への変換、PMA RGBAのoverlay、libx264 CRF26 / veryfast / threads8、filter threads2、AAC128k、MP4 faststart 完了を含む。GPUエンコーダーは使用しない。','- 本番アプリの計時ではない。コメント取得とブラウザ生成はネイティブ比較の外であり、別計測の時間を単純に足して本番の処理時間としない。テスト用FFmpegフィルターは60fps化をoverlay前に行い、PMAを明示。既存本番引数全体をそのまま再生した比較ではない。','',
'## 合成単独とエンコード込み','',
'|素材|計測|方式|中央値 秒|最小–最大 秒|基準からの時間短縮|','|---|---|---|---:|---:|---:|']
labels={'baseline':'WARP基準','instanced':'まとめ描画','crop':'余白切り詰め','hardware':'実GPU','hardware-instanced':'実GPU＋まとめ'}
mode_labels={'compose':'合成単独','encode':'エンコード込み','pipe':'RGBA受渡しのみ','overlay':'映像合成・色変換まで'}
for r in s['native']:
    base=next(x for x in s['native'] if x['fixture']==r['fixture'] and x['mode']==r['mode'] and x['variant']=='baseline')
    change=(1-r['median_s']/base['median_s'])*100
    lines.append(f"|{r['fixture']}|{mode_labels[r['mode']]}|{labels[r['variant']]}|{r['median_s']:.3f}|{r['min_s']:.3f}–{r['max_s']:.3f}|{change:+.1f}%|")
lines+=['','短縮率が負なら遅くなった。参考値であり統計的有意差を主張しない。RGBA受渡しのみ / 映像合成・色変換までは原因切り分けの追加検査で各3回。前者は元動画なしのraw RGBA入力→null、後者は同じ元動画・overlay・YUV420p変換→null（H.264/AACエンコードなし）。各モードの差分は並行処理や待ち方も変わるため、処理の厳密な排他的内訳として引き算しない。','',
'## 処理区間（各区間の中央値、ms）','',
'|素材・計測|方式|描画投入 CPU|Map 待ち|出力・受取待ち|入力読み込み|テクスチャ作成|','|---|---|---:|---:|---:|---:|---:|']
for r in s['native']:
    q=r['profile_median']
    lines.append(f"|{r['fixture']} / {r['mode']}|{labels[r['variant']]}|{q['draw_submit_ms']:.2f}|{q['map_wait_ms']:.2f}|{q['output_ms']:.2f}|{q['input_read_ms']:.2f}|{q['texture_ms']:.2f}|")
lines+=['','Map はその staging スロットの描画・コピー完了を待つ時間を含む。これだけでは GPU 実行時間と転送時間を分離できない。GPU timestamp は今回未実装であり、CPU の描画投入時間を GPU 実行時間としていない。出力時間は fwrite のコピーと FFmpeg の受取待ちを含み、転送だけの速度ではない。中央値同士の和は全体中央値と一致しない。','',
'## 文字画像生成とブラウザ側','',
'こちらは別途、実コメントスナップショットを現在のソースで読み込んで生成した。冷たいブラウザを毎回起動、60fps、30フレーム単位、lossless palette + Deflate。各5回。固定 native fixture のテクスチャ個数とは別の測定である。','',
'|対象|起動|初期化待ち|draw 呼出し合計|take / 圧縮・JSON受渡し|Go展開|Go書出し|','|---|---:|---:|---:|---:|---:|---:|']
browser_notes=[]
for duration in sorted({r['duration_ms'] for r in s['browser']}):
    g=[r for r in s['browser'] if r['duration_ms']==duration]
    keys=['startup_ms','wait_ready_ms','total_drawMs','total_takeMs','total_decodeMs','total_writeToDiscardMs']
    lines.append('|'+f'{duration/1000:.3f}秒 / {len(g)}回'+'|'+ '|'.join(f'{med([r[k] for r in g])/1000:.3f}秒' for k in keys)+'|')
    calls=[r['browser_calls_total'] for r in g]
    browser_notes+=['',f"{duration/1000:.3f}秒の内訳: テクスチャ {med([r['total_textures'] for r in g]):g}枚、文字幅測定 {med([r['measureTextMs'] for r in calls]):.1f}ms、strokeText {med([r['strokeTextMs'] for r in calls]):.1f}ms、fillText {med([r['fillTextMs'] for r in calls]):.1f}ms、texImage2D {med([r['texImage2DMs'] for r in calls]):.1f}ms、readPixels {med([r['readPixelsMs'] for r in calls]):.1f}ms。",'']
lines+=browser_notes
lines+=['Canvas2D の文字描画 API は描画を遅延実行できるため、strokeText/fillText の時間を文字のラスタライズ完了時間と同一視しない。texImage2D/readPixels など後続処理でその負担が見える可能性がある。ブラウザ内 API 時間は初期化・draw 区間に内包され、上表に重ねて加算しない。timer の分解能とフック自体の負担もある。take は圧縮・Base64・JSON・CDP受渡し・Go JSON展開をまとめた計測で、圧縮だけが遅いと断定していない。','',
'## 品質','',
'|素材|方式|比較フレーム|最大チャンネル差 0–255|変化チャンネル数|','|---|---|---:|---:|---:|']
for q in s['quality']:
    if not q['original'] and q['variant']!='crop':continue
    lines.append(f"|{q['fixture']}|{labels[q['variant']]}|{q['frames']}|{q['max_channel_diff']}|{q['changed_channels']:,}|")
lines+=['',
'まとめ描画は各 command の rect・projection・color と順序を維持し、RGBA 全フレーム一致。色・半透明・重なり・clip・負の幅・59.94fps の人工ストレスでも一致を確認。実GPUと実GPU＋まとめは REAL / stress で同一出力だが、WARPとは丸め差がある。減色による差ではないものの、完全一致の採用条件は満たさない。','',
'余白切り詰めは1 texelの補間余白を残し、元の座標へ対応させたが画素差が残ったため、この実装を本番採用しない。テクスチャのバイト削減は REAL 22.8%、F1 28.0%。','',
'## 判定と次に狙う箇所','',
'1. 文字画像の初回生成だけが支配的、という仮説はこの素材では支持されない。幅測定・テクスチャ化を含めても、ブラウザでの取り出し・受渡し、ネイティブの読み戻しと出力待ちが大きい。',
'2. まとめ描画は呼出しを REAL 15,738→7,573、F1 21,420→1,887 に減らせ、画素一致は達成した。しかし全面合成・読み戻し・エンコードを含めた高速化はケース依存。本番へ自動反映する根拠にしない。',
'3. 実GPUは合成単独では有望。追加切り分けでは、REALの実GPUでRGBA受渡しまで中央値1.56秒、元動画のデコード・拡大・合成・色変換まで5.57秒、エンコード込み7.53秒。この実験ではFFmpeg側の映像処理が大きい。次はそのうちscale / overlay / 色変換の内訳を調べ、元動画とコメントの合成・色変換までGPUで行ってlibx264へ渡す構成を比較候補にする。速度・画素一致はまだ未検証。既存のコピー削減方針とも整合する。',
'4. ブラウザ側は take の内訳を分解し、描画とtakeの一括要求、繰り返される行列を含む command JSON のバイナリ化を次の比較候補にする。これは次段階の仮説であり、今回の実測による採用済み最適化ではない。',
'5. 余白切り詰めは見送り。A1/A2/A4 の白コメント減色も引き続き不採用。','',
'## 検査・成果物','']
for filename,label in [('malformed.json','不正 NPS3'),('video-validation.json','MP4 decode / PTS / frame数 / 1 slice per AU')]:
    path=BASE/filename
    if path.exists():lines.append(f'- {label}: {len(json.loads(path.read_text(encoding="utf8")))}件の記録。')
lines+=['- 実験中に見つかった instance offset の不具合は b1 の明示 base index で修正。修正前の失敗は failures/ に残した。','- 実験ソース: scripts/experiments/nico-native-probe/、ブラウザ試験: internal/nicorender/native_profile_test.go（通常ビルド対象外）。','- 生データ: build/nico-native-probe/。各 config.json に argv、scene/exe/FFmpeg/source SHA256、results に終了後の区間計測。','- 稼働中アプリ PID 36316 の再起動・置換、本番コードの変更、commit/push はしていない。','',
'参考: [Microsoft DrawInstanced](https://learn.microsoft.com/en-us/windows/win32/api/d3d11/nf-d3d11-id3d11devicecontext-drawinstanced)。APIは提出したプリミティブを描画するものであり、呼出し回数の減少だけで全体性能が保証されるものではない。','']
out=ROOT/'docs/verification/niconico-comments/native-render-2026-09-17.md';out.parent.mkdir(parents=True,exist_ok=True);out.write_text('\n'.join(lines),encoding='utf8');print(out)
