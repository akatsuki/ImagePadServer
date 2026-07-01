# 同一URLのRTSPをPC,Android両方のVRChatに対応させた話

## はじめに - ImagePadServerの紹介

ImagePadServerは、VRChat上で画像や動画を扱いやすくするために作っているローカルサーバーです。

PC上で起動して、画像や動画を受け取り、VRChat内のプレイヤーやワールドから参照できるURLとして配信します。最初は画像共有が中心でしたが、次第に「動画も流したい」「できれば低遅延で流したい」という要求が出てきました。

そして低遅延配信に手を出した結果、HLS、LL-HLS、LHLS、RTSP、RTSP over TCP、Android対応、プロキシ振り分けという順番で地獄を見ることになりました。

この記事は、その失敗の記録です。

## 友人のディスり - 遅延がひどくて使い物にならない

きっかけは友人からの一言でした。

「遅延がひどくて使い物にならない」

こちらとしては、動画が再生できている時点でかなり満足していました。URLを渡せばVRChat内で動画が再生される。技術的には一歩前進です。

しかし、使う側からすると「再生できる」と「使える」は別物でした。

特にライブ的に画面や映像を見せたい用途では、数秒どころか10秒近い遅延はかなり気になります。操作してから反映されるまでが遅い。会話と映像が噛み合わない。見せたいタイミングで見せられない。

つまり、配信としては成立していても、体験としては失敗でした。

## HLSという甘美な罠

最初に選んだのはHLSでした。

HLSはとにかく扱いやすいです。HTTPで配れる。ブラウザや動画プレイヤーで扱いやすい。FFmpegでも簡単に出せる。セグメント化された `.ts` と `.m3u8` を置けば、それっぽい配信がすぐ動きます。

実装も安定していました。

ImagePadServer側で動画を受け取り、FFmpegでHLSに変換し、VRChat側には `.m3u8` のURLを渡す。構成としては素直です。

しかし、問題は遅延でした。

HLSは基本的にセグメントを作って、それをプレイヤーが追いかける方式です。安定性のためにある程度バッファを持ちます。結果として、手元の構成ではだいたい10秒前後の遅延が出ました。

動画を見るだけなら問題ありません。

でも、低遅延で見せたい用途では厳しい。

ここで「HLSは簡単で安定しているが、低遅延ではない」という現実にぶつかりました。

## LL-HLSとLHLSという次の罠

では低遅延HLSを使えばいいのでは、となります。

そこで出てくるのがLL-HLSとLHLSです。

名前だけ見ると希望があります。Low-Latency HLS。低遅延のためのHLS。今のHLS実装を少し変えればいけるのでは、と思ってしまいます。

しかし、ここが次の罠でした。

LL-HLSとLHLSは名前は似ていますが、実装としては別物です。Apple系のLL-HLS、コミュニティ由来のLHLS、それぞれ前提やプレイヤー対応が違います。

さらに、FFmpegで「それっぽいもの」を出すことはできても、対象プレイヤーが期待する完全な形になるとは限りません。部分セグメント、プレイリスト更新、プリロードヒント、チャンク転送など、低遅延化のために必要な要素が増えます。

通常のHLSのように「FFmpegでm3u8を出せば終わり」とはいきませんでした。

低遅延に近づけようとすると、配信サーバーとプレイヤーの対応状況をかなり正確に合わせる必要があります。

## 非対応という地獄

そして、ふたを開けてみるとVRChat側では期待通りに動きませんでした。

LL-HLSもLHLSも、こちらが期待する低遅延モードとしては扱われませんでした。結局、互換モードのような挙動になり、通常のHLSと同じようにバッファされます。

結果、遅延はあまり縮まりません。

ここがかなりつらいところです。

実装側では低遅延HLSのつもりで頑張っている。プレイリストも工夫する。セグメントも短くする。FFmpegの設定も詰める。

でも、最終的に再生するVRChat側がそれを低遅延HLSとして扱ってくれなければ意味がありません。

「規格上できる」と「そのプレイヤーで低遅延再生できる」は別問題でした。

この時点で、HLS系で粘るのはやめた方がよさそうだと判断しました。

## RTSPの解析

そこで、重い腰を上げてRTSPを調べ始めました。

RTSPはHLSと比べると扱いづらいです。HTTPで `.m3u8` とセグメントを置けば終わり、という世界ではありません。接続、セッション、トランスポート、RTP、RTCPなど、考えることが増えます。

ただ、VRChatのプレイヤー実装やログを見ていくと、RTSPは候補としてかなり有力でした。

PC版VRChatではAVPro経由で `rtsp://` や `rtspt://` が開けることが分かりました。ローカルログでも、RTSP URLは再生開始まで到達していました。

ここで重要だったのが、RTSPの「URL」と「実際のメディア転送」は別物だという点です。

RTSPでは、制御用の接続とは別に、映像や音声をRTPで流します。このRTPをUDPで流すこともできますし、RTSPのTCP接続の中にinterleavedで流すこともできます。

PC版VRChatでは、実質的にRTSP/TCPの経路が安定していました。`rtspt://` でも動き、`rtsp://` でもTCPを選ばせれば動く。つまりPC側はTCP固定に寄せるのが正解に見えました。

一方で、Android/Quest版は話が違いました。

Android/Quest版VRChatの動画プレイヤーは内部でExoPlayer系の挙動を持っており、RTSPではUDP側に寄る挙動が出ます。RTSPをUDPで通そうとすると、NAT越えが問題になります。

UDPのRTPは、HTTPやRTSP/TCPのように単純な1本のTCP接続ではありません。ルーターのNATを越えるときにポート対応が崩れやすく、外から見えるアドレスと内側の端末が期待するポートが一致しないことがあります。

その結果、Android/Quest側ではRTSP接続を開始しても、UDPのメディア経路が成立せず、接続エラーやタイムアウトを繰り返します。

プレイヤーによっては、UDPで失敗した後にTCPへフォールバックすることがあります。しかし、このフォールバックを待つと遅い。低遅延配信をしたいのに、接続開始時点でタイムアウト待ちが発生します。

ここで一度、考え方は単純になりました。

「じゃあ最初からTCPに固定すればいい」

PCではRTSP/TCPが安定している。Android/QuestでもUDPがNATで詰まるなら、最初からRTSP/TCPに寄せればよい。URLも `rtsp://` のまま、サーバー側でTCPだけを受けるようにすれば、UDPの失敗待ちを避けられるはずです。

ImagePadServer側では、MediaMTXをRTSP/TCP用に立て、FFmpegからもRTSP/TCPでpublishする構成にしました。

```yaml
rtsp: yes
rtspTransports: [tcp]
rtspEncryption: "no"
rtspAddress: :8554
```

FFmpeg側もTCPで投入します。

```bash
ffmpeg \
  -i rtmp://0.0.0.0:1935/live/stream \
  -fflags nobuffer \
  -flags low_delay \
  -c:v libx264 \
  -preset ultrafast \
  -tune zerolatency \
  -c:a aac \
  -f rtsp \
  -rtsp_transport tcp \
  rtsp://pub:secret@127.0.0.1:8554/live_xxxx
```

この時点では、かなり筋が良く見えました。

PCはTCPで動く。Android/QuestはUDPで詰まる。ならばRTSPをTCP固定にすれば、両方を同じURLで処理できるはず。

しかし、ここで次の罠を踏みました。

Android/Quest側は、RTSPをTCPモードにすると今度は無視し始めたのです。

## RTSP(TCP)はAndroidで無視される

RTSPをTCPモードにすると、Android側が無視を始めました。

PCでは動く。Androidでは動かない。

低遅延配信で一番見たくないやつです。

なぜ無視されるのかは、VRChatの内部実装を完全に追い切れているわけではないので断定はできません。ただ、挙動から見ると、Android/Quest側のRTSP実装がPC版AVProのように `rtspt://` やRTSP interleaved TCPを素直に扱っていない可能性が高そうでした。

PC版VRChatではAVProがMediaFoundation経由で動いており、RTSP/TCPの経路が比較的はっきり見えます。一方で、Android/Quest側はExoPlayer系のRTSP処理に寄っていると考えると、同じ `rtsp://` URLでも内部のトランスポート選択やフォールバックの挙動が違っていても不思議ではありません。

さらに、RTSP/TCP固定はサーバー側から見ると「TCPだけ受ければいい」ので単純ですが、クライアント側から見ると「このRTSPサーバーはUDPを提示しない」「TCP interleavedで進める必要がある」という状態になります。Android側の実装がUDP前提で接続を始めたり、UDPでのSETUPを期待したり、TCP interleavedへの切り替えを十分に扱わない場合、プレイヤーは明示的なエラーを出すより先に接続を諦めたように見えます。

つまり、Androidが本当に「TCPを知らない」というより、VRChat Android/Quest版の動画プレイヤーが期待するRTSPの成立手順と、こちらが用意したRTSP/TCP固定のサーバー挙動が噛み合っていない、という見方です。

ここで厄介なのは、`rtsp://` というURLだけを見ても、この差が分からないことです。URLは同じでも、PCはTCPで読みに来る。AndroidはUDP寄りの手順を期待する。NAT越えの問題を避けるためにTCP固定にしたのに、そのTCP固定がAndroid側の入口で弾かれる。

結果として、「RTSPは使える」「TCPならNATを避けられる」「同じURLにできる」という個別の判断はそれぞれ正しそうなのに、全部を組み合わせるとAndroidだけ沈黙する、という状態になりました。

ここで単純に考えると、PC用URLとAndroid用URLを分けたくなります。PCにはTCP向け、AndroidにはUDP向け、というように。

でも、それをやるとワールド側やユーザー操作が面倒になります。

PCかAndroidかを判定してURLを変える。同期対象のURLが変わる。QRコードやコピーURLも分かれる。トラブル時に「どっちのURLを使っているか」を確認しないといけない。

この時点で、URL分岐は最後の手段にしたいと考えました。

## RTSPの前に門番を置く

AndroidでRTSP/TCPが素直に動かないことが分かったので、最初はURLを分ける案を考えました。

PC向けにはTCP前提のURL、Android向けにはAndroidが受け入れる経路のURL、という形です。

しかし、それをやるとVRChat側に分岐が漏れます。ワールド側でPC/Androidを意識する必要が出るし、共有URLも2種類になります。これは避けたい。

そこで、RTSPサーバーの前に小さなプロキシを置くことにしました。

構成としてはこうです。

```text
VRChat Player
  -> rtsp://example.com/live/xxxx
  -> ImagePadServer RTSP Proxy
      -> PC向け: RTSP/TCP固定のMediaMTX
      -> Android向け: UDP/TCP両対応の標準RTSPサーバー
  -> FFmpegから投入されたH.264/AACストリーム
```

プレイヤーから見ると、接続先は常に同じです。

```text
rtsp://example.com/live/xxxx
```

PCでもAndroidでも同じURLを入れます。

違いは、URLの奥にいる門番が吸収します。プレイヤーがRTSP接続を開始した瞬間に、プロキシが最初のRTSPリクエストを読みます。

RTSPでは、接続直後にだいたい次のようなリクエストが飛んできます。

```text
OPTIONS rtsp://example.com/live/xxxx RTSP/1.0
CSeq: 1
User-Agent: ...
```

ここでプロキシは、まだ映像データを流し始めません。まず接続元の情報や `User-Agent`、RTSPの要求内容を見ます。

実装イメージはこうです。

```go
func handleRTSPConn(client net.Conn) {
    defer client.Close()

    firstRequest, buffered, err := readFirstRTSPRequest(client)
    if err != nil {
        return
    }

    backend := chooseBackend(firstRequest)

    upstream, err := net.Dial("tcp", backend.Address)
    if err != nil {
        return
    }
    defer upstream.Close()

    // 最初に読んでしまったRTSPリクエストを、上流へ戻す
    upstream.Write(buffered)

    // 以降はただのTCP中継
    go io.Copy(upstream, client)
    io.Copy(client, upstream)
}
```

ポイントは、プロキシがRTSPの中身を全部理解する必要はないことです。

必要なのは最初の入口だけです。最初の `OPTIONS` や `DESCRIBE` を少し見て、どちらのバックエンドに流すかを決めたら、あとはTCPストリームをそのまま中継します。

ここで振り分け先の性格を変えます。

PC向けには、これまで通りRTSP/TCP固定のサーバーへ流します。PC版VRChatはこの経路が安定していたので、UDPを提示せず、最初からTCP interleavedで読ませます。

Android向けには、TCP固定ではなく、標準的なRTSPサーバーへ流します。つまり、UDPとTCPの両方を用意しておき、Android/Quest側のプレイヤーが期待するRTSPの手順に寄せます。サーバー側が勝手にTCPだけに絞るのではなく、Android側が扱いやすい通常のRTSPとして見せる、という考え方です。

外から見るURLは同じですが、門番の奥はこう分かれます。

```text
rtsp://example.com/live/xxxx
  -> RTSP Proxy
      -> PC判定      -> rtsp://127.0.0.1:8554/live/xxxx  (TCP固定)
      -> Android判定 -> rtsp://127.0.0.1:8555/live/xxxx  (UDP/TCP両対応)
```

つまり、門番の仕事はこれだけです。

```go
func chooseBackend(req RTSPRequest) Backend {
    ua := strings.ToLower(req.Header.Get("User-Agent"))

    if strings.Contains(ua, "android") ||
       strings.Contains(ua, "exoplayer") {
        return Backend{
            Address: "127.0.0.1:8555", // Android向け: UDP/TCP両対応RTSP
        }
    }

    return Backend{
        Address: "127.0.0.1:8554", // PC向け: RTSP/TCP固定
    }
}
```

実際の配信本体はMediaMTXに任せます。

ImagePadServer側では、FFmpegでOBSなどから来た入力をH.264/AACに変換し、MediaMTXへRTSP/TCPでpublishします。

```go
args := []string{
    "-i", inputURL,

    "-c:v", "libx264",
    "-preset", "ultrafast",
    "-tune", "zerolatency",
    "-g", gopFrames,
    "-keyint_min", gopFrames,
    "-sc_threshold", "0",

    "-c:a", "aac",
    "-ar", "48000",
    "-ac", "2",

    "-f", "rtsp",
    "-rtsp_transport", "tcp",
    "rtsp://pub:secret@127.0.0.1:8554/live/xxxx",
}
```

PC向けのMediaMTXは、RTSP/TCP固定にします。

```yaml
rtsp: yes
rtspTransports: [tcp]
rtspEncryption: "no"
rtspAddress: :8554

paths:
  live_xxxx:
    source: publisher
```

一方でAndroid向けのRTSPサーバーは、UDPとTCPの両方を提示できる標準的な構成にします。

```yaml
rtsp: yes
rtspTransports: [udp, tcp]
rtspEncryption: "no"
rtspAddress: :8555

paths:
  live_xxxx:
    source: publisher
```

ここでやっているのは、Androidに特別なURLを渡すことではありません。Androidには同じ `rtsp://example.com/live/xxxx` を渡したまま、門番の奥で「Androidが期待する普通のRTSPサーバー」へ案内しています。

PCにはTCP固定の安定経路を見せる。AndroidにはUDP/TCP両対応の標準経路を見せる。どちらも入口URLは同じ。

この分離によって、VRChatワールドやユーザー操作には分岐を見せず、配信基盤の中だけでPC/Android差分を吸収できるようになりました。

この構成にすると、プレイヤーは常に同じURLへ接続します。

```text
rtsp://example.com/live/xxxx
```

しかし、その手前ではプロキシが接続を受け、PCならPC向け、AndroidならAndroid向けの経路へ流します。

ここで大事なのは、**振り分けをRTSP接続が始まる前、正確にはRTSPセッションが成立する前に終わらせる**ことです。

再生が始まった後に切り替えるのでは遅いです。RTPの流れが始まってから触ると、プレイヤー側から見ると壊れたストリームになります。

だから、門番は最初のRTSPリクエストだけを見ます。

```text
1. TCP接続を受ける
2. 最初のRTSPリクエストを読む
3. PC/Android向けバックエンドを選ぶ
4. 読んだリクエストを選んだバックエンドへ転送する
5. 以降は双方向にio.Copyするだけ
```

この方式なら、プレイヤーは裏で何が起きているかを知りません。

PCもAndroidも同じ `rtsp://` URLを開いているだけです。URL分岐はありません。VRChatワールド側にも分岐は漏れません。

結果として、互換性の差分はImagePadServer側の配信基盤に閉じ込められます。

これが、RTSPの前に門番を置いた理由です。

## 接続成功！

最終的に、この方式でPCとAndroidの両方から同じRTSP URLで接続できるようになりました。

ここまで来るのに、HLS、LL-HLS、LHLS、RTMP、RTSPT、RTSP over TCP、Androidの挙動差を踏み抜きました。

振り返ると、最初からRTSPプロキシ構成にたどり着けたわけではありません。

HLSは簡単で安定していましたが、遅延が大きすぎました。LL-HLSとLHLSは希望に見えましたが、VRChat側の対応で詰まりました。RTSPは有力でしたが、PCとAndroidで挙動が分かれました。

そして最終的に、URLを分けるのではなく、RTSPの前にプロキシを置いて同一URLを維持する形に落ち着きました。

今回の教訓は、低遅延配信ではプロトコル名だけを見ても駄目だということです。

大事なのは、実際のプレイヤーがどう接続し、どこでバッファし、どの経路なら再生開始まで到達するかです。

そしてVRChat向けには、技術的に動くことだけでなく、ワールド側やユーザーから見て扱いやすいことも重要でした。

同一URLでPCとAndroidの両方に対応できたのは、単なる実装上の工夫ではなく、運用と体験を壊さないための設計判断でした。

## まとめ

今回分かったクライアントごとの挙動は、ざっくりこうです。

```text
PC版VRChat
  -> RTSP/TCPが安定
  -> rtspt:// や RTSP interleaved TCP の経路が使える
  -> TCP固定のRTSPサーバーへ流すのが扱いやすい

Android/Quest版VRChat
  -> PC版と同じRTSP/TCP固定では素直に動かない
  -> UDP寄り、または標準的なRTSP手順を期待しているように見える
  -> UDP/TCP両対応のRTSPサーバーへ流す方が噛み合いやすい

HLS/LL-HLS/LHLS
  -> 実装や配信はしやすい
  -> ただしVRChatでは低遅延再生として期待通りに扱われない場合がある
  -> 結果として10秒前後の遅延や互換モードに戻りやすい
```

解決策は、URLを分けることではありませんでした。

ユーザーやVRChatワールドから見えるURLは、最後まで1つにします。

```text
rtsp://example.com/live/xxxx
```

その代わり、RTSPの入口にプロキシを置きます。

プロキシは接続開始時にクライアントを見て、PCならRTSP/TCP固定の経路へ、Android/QuestならUDP/TCP両対応の標準RTSP経路へ案内します。

```text
同一URL
  -> RTSPプロキシ
      -> PC: RTSP/TCP固定
      -> Android/Quest: UDP/TCP両対応RTSP
```

これで、PCとAndroidの挙動差を配信基盤側に閉じ込められます。

VRChat側には同じURLだけを見せる。  
プロトコル差分はプロキシの奥で吸収する。

これが、同一URLのRTSPをPC版とAndroid版の両方のVRChatに対応させるための最終的な解決策でした。
