# ImagePad source-clock UxPlay

このディレクトリは、ImagePadServer用に固定したUxPlay Windows wrapperと
`libuxplay`の出所、適用patch、再現ビルド手順を管理する。

- wrapper: `leapbtw/uxplay-windows` commit `43cf903e8177a7db37df6bc9cd8a9d1a60dabaaa`
- library: `leapbtw/libuxplay` commit `437f37514257d9cb513ac7fbdee743b4da85852e`
- reference: `FDH2/UxPlay` commit `fd5192b06f0c2ab2f664b8ab778ac6f772e4d741`
- license: GPL-3.0-or-later

`patches/0001-imagepad-source-clock-egress.patch` は、UxPlay callbackで受けた
元remote NTPとcomplete frameを二つのloopback TCPへ送る変更を含む。installed
moduleは変更せず、`scripts/build-uxplay-source-clock.ps1`が一時source checkout
へpatchを適用して別のqualification outputへ出力する。

追加patchは、`0002`がH.265 payload境界、`0003`がplist response所有権、
`0004`がlibplist返却値のallocator対応解放、`0005`がTCP断片到着時の
RTSP protocol文字列組み立て、`0006`がwriterのidle wait、`0007`が
headless source-clock受信時の要求FPS保持を修正する。`0004`と`0005`には
それぞれ独立したnative CTestがあり、package manifestの`plist-owned-free`と
`fragment-safe-rtsp` capabilityに対応する。`0007`は実際の`/info`応答を読む
`scripts/test-uxplay-headless-fps.ps1`で検証する。

source-clock receiverは次の引数を提供する。

```text
-ipscv 127.0.0.1:PORT
-ipsca 127.0.0.1:PORT
-ipsct 32_HEX_CHARS
```

改変UxPlayの配布には、上記source情報、適用patch、対応するGPL source、build
ログ、capability manifestを同じrelease単位で含める。production qualification
前は明示的な絶対pathからのみ起動する。
