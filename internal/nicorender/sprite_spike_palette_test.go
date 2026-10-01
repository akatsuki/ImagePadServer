//go:build nico_sprite_spike

package nicorender

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func b64(v []byte) string { return base64.StdEncoding.EncodeToString(v) }

func TestDecodeSpikeTexturePackedForms(t *testing.T) {
	if got, err := decodeSpikeTexture(spikeTexture{Width: 2, Height: 2, Data: b64([]byte{0, 1, 2, 3}), Encoding: "palette8", Palette: b64([]byte{0, 0, 0, 0, 255, 0, 128, 64, 12, 34, 56, 78, 255, 255, 255, 255})}); err != nil {
		t.Fatal(err)
	} else if want := []byte{0, 0, 0, 0, 255, 0, 128, 64, 12, 34, 56, 78, 255, 255, 255, 255}; string(got) != string(want) {
		t.Fatalf("palette = %v, want %v", got, want)
	}
	if got, err := decodeSpikeTexture(spikeTexture{Width: 2, Height: 1, Data: b64([]byte{0, 128, 17, 255}), Encoding: "grayalpha8"}); err != nil {
		t.Fatal(err)
	} else if want := []byte{0, 0, 0, 128, 17, 17, 17, 255}; string(got) != string(want) {
		t.Fatalf("grayalpha = %v, want %v", got, want)
	}
	raw := []byte{0, 1, 2, 3, 255, 128, 0, 64}
	if got, err := decodeSpikeTexture(spikeTexture{Width: 2, Height: 1, Data: b64(raw), Encoding: "rgba"}); err != nil || string(got) != string(raw) {
		t.Fatalf("raw = %v, %v", got, err)
	}
}

func TestDecodeSpikeTextureRejectsMalformed(t *testing.T) {
	cases := []spikeTexture{
		{Width: 16385, Height: 1, Data: b64(make([]byte, 4))},
		{Width: 1, Height: 1, Data: b64([]byte{0, 0, 0}), Encoding: "rgba"},
		{Width: 1, Height: 1, Data: b64([]byte{0}), Encoding: "palette8", Palette: b64([]byte{0, 0, 0})},
		{Width: 1, Height: 1, Data: b64([]byte{1}), Encoding: "palette8", Palette: b64([]byte{0, 0, 0, 255})},
		{Width: 1, Height: 1, Data: b64([]byte{0, 0}), Encoding: "unknown"},
		{Width: 1, Height: 1, Data: b64([]byte{0, 0}), Encoding: "grayalpha8", Palette: b64([]byte{0, 0, 0, 0})},
		{Width: 1, Height: 1, Data: b64([]byte{0, 0, 0, 255}) + "\n", Encoding: "rgba"},
		{Width: 1, Height: 1, Data: b64([]byte{0}), Encoding: "palette8", Palette: string(make([]byte, 1369))},
	}
	for i, tex := range cases {
		if _, err := decodeSpikeTexture(tex); err == nil {
			t.Errorf("case %d accepted malformed texture", i)
		}
	}
}

func TestSpikeJSPackingRoundTripsThroughGo(t *testing.T) {
	makePixels := func(colors [][4]byte) []byte {
		out := make([]byte, len(colors)*4)
		for i, c := range colors {
			copy(out[i*4:], c[:])
		}
		return out
	}
	gray := make([]byte, 257*4)
	for i := 0; i < 256; i++ {
		gray[i*4], gray[i*4+1], gray[i*4+2], gray[i*4+3] = byte(i), byte(i), byte(i), 255
	}
	gray[256*4], gray[256*4+1], gray[256*4+2], gray[256*4+3] = 0, 0, 0, 128
	exact256 := make([][4]byte, 256*3)
	for i := range exact256 {
		exact256[i] = [4]byte{byte(i / 3), byte(i/3 ^ 0x55), byte(i/3 ^ 0xaa), 255}
	}
	colored257 := append(append([][4]byte{}, exact256...), [4]byte{1, 2, 3, 4})
	cases := [][]byte{
		makePixels([][4]byte{{0, 0, 0, 0}, {255, 255, 255, 128}, {12, 34, 56, 255}, {0, 0, 0, 255}, {0, 0, 0, 0}, {255, 255, 255, 128}, {12, 34, 56, 255}, {0, 0, 0, 255}}),
		gray,
		makePixels(exact256),
		makePixels(colored257),
	}
	jsonCases := make([][]int, len(cases))
	for i, c := range cases {
		jsonCases[i] = make([]int, len(c))
		for j, v := range c {
			jsonCases[i][j] = int(v)
		}
	}
	payload, _ := json.Marshal(jsonCases)
	jsPath, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	jsPath = filepath.Join(jsPath, "sprite_spike.js")
	pathJSON, _ := json.Marshal(jsPath)
	script := "const fs=require('fs'),vm=require('vm');let window={__niconi:{renderer:{rendererName:'WebGL2Renderer',gl:{bindTexture(){},useProgram(){},uniform4f(){},uniform1f(){},uniformMatrix4fv(){},drawArrays(){},getParameter(){},createFramebuffer(){},bindFramebuffer(){},framebufferTexture2D(){},checkFramebufferStatus(){},deleteFramebuffer(){},TEXTURE_2D:1,FRAMEBUFFER:2,FRAMEBUFFER_BINDING:3,COLOR_ATTACHMENT0:4,FRAMEBUFFER_COMPLETE:5,TRIANGLE_STRIP:6,RGBA:7,UNSIGNED_BYTE:8},_createTexture(){return {}},flush(){},spriteProg:{},rectProg:{},spriteLocRect:{},spriteLocProj:{},spriteLocAlpha:{},rectLocRect:{},rectLocProj:{},rectLocColor:{}}}};vm.runInNewContext(fs.readFileSync(" + string(pathJSON) + ",'utf8'),{window,btoa:s=>{let a=[];for(let i=0;i<s.length;i++)a.push(s.charCodeAt(i));return Buffer.from(a).toString('base64')}});window.__spikePack=true;let cases=" + string(payload) + ";console.log(JSON.stringify(cases.map(p=>window.__spikePackTextureForTest(new Uint8Array(p),p.length/4,1))));"
	out, err := exec.Command("node", "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("node codec: %v: %s", err, out)
	}
	var packed []struct{ Data, Encoding, Palette string }
	if err = json.Unmarshal(out, &packed); err != nil {
		t.Fatal(err)
	}
	if len(packed) != len(cases) {
		t.Fatalf("packed case count=%d", len(packed))
	}
	wantEnc := []string{"palette8", "grayalpha8", "palette8", ""}
	for i, p := range packed {
		if p.Encoding != wantEnc[i] {
			t.Errorf("case %d encoding=%q want %q", i, p.Encoding, wantEnc[i])
		}
		got, err := decodeSpikeTexture(spikeTexture{Width: uint32(len(cases[i]) / 4), Height: 1, Data: p.Data, Encoding: p.Encoding, Palette: p.Palette})
		if err != nil {
			t.Fatalf("case %d decode: %v", i, err)
		}
		if !bytes.Equal(got, cases[i]) {
			t.Errorf("case %d roundtrip mismatch", i)
		}
	}
}
