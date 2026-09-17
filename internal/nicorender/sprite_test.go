package nicorender

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/niconico"
)

func TestSpriteOptionsEnforceProductionBounds(t *testing.T) {
	base := RenderOptions{Width: 320, Height: 180, DurationMs: 1000, FPSNum: 30, FPSDen: 1}
	if err := validateSpriteOptions(base); err != nil {
		t.Fatal(err)
	}
	for name, options := range map[string]RenderOptions{
		"numerator":   withFPS(base, 1000001, 1),
		"denominator": withFPS(base, 1, 1000001),
		"zero frames": RenderOptions{Width: 320, Height: 180, DurationMs: 0, FPSNum: 30, FPSDen: 1},
		"frames":      RenderOptions{Width: 320, Height: 180, DurationMs: 100000000000, FPSNum: 30, FPSDen: 1},
	} {
		if err := validateSpriteOptions(options); err == nil {
			t.Errorf("%s bound accepted", name)
		}
	}
}

func withFPS(o RenderOptions, num, den int64) RenderOptions {
	o.FPSNum, o.FPSDen = num, den
	return o
}

func TestWriteSpriteStreamRejectsNilWriterBeforeBrowserStartup(t *testing.T) {
	_, err := WriteSpriteStream(context.Background(), niconico.Snapshot{}, RenderOptions{}, nil)
	if err == nil {
		t.Fatal("nil writer accepted")
	}
}

func TestSpriteIDsAreStrictlyMonotonicAcrossBatchAndRetirement(t *testing.T) {
	active := map[uint32]int64{1: 4}
	if err := validateSpriteIDs([]spriteTexture{{ID: 3, Width: 1, Height: 1, RGBA: make([]byte, 4)}, {ID: 2, Width: 1, Height: 1, RGBA: make([]byte, 4)}}, nil, active, 8, 1); err == nil {
		t.Fatal("out-of-order IDs accepted")
	}
	if err := validateSpriteIDs([]spriteTexture{{ID: 1, Width: 1, Height: 1, RGBA: make([]byte, 4)}}, nil, map[uint32]int64{}, 0, 3); err == nil {
		t.Fatal("retired ID reused")
	}
}

func TestSpriteJSFlushCapturesDrawsInsideOriginalFlushAndPacksByDefault(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	const script = `
const fs=require('fs'), vm=require('vm'), zlib=require('zlib');
const gl={TEXTURE_2D:1,FRAMEBUFFER:2,FRAMEBUFFER_BINDING:3,COLOR_ATTACHMENT0:4,FRAMEBUFFER_COMPLETE:5,TRIANGLE_STRIP:6,RGBA:7,UNSIGNED_BYTE:8,
  bindTexture(){},useProgram(){},uniform4f(){},uniform1f(){},uniformMatrix4fv(){},drawArrays(){},deleteTexture(){},getParameter(){return null},createFramebuffer(){return {}},bindFramebuffer(){},framebufferTexture2D(){},checkFramebufferStatus(){return 5},readPixels(){},deleteFramebuffer(){}};
const r={rendererName:'WebGL2Renderer',gl,_createTexture(){return {}},spriteProg:{},rectProg:{},spriteLocRect:{},spriteLocProj:{},spriteLocAlpha:{},rectLocRect:{},rectLocProj:{},rectLocColor:{}};
r.flush=()=>{gl.useProgram(r.rectProg);gl.uniform4f(r.rectLocRect,1,2,3,4);gl.uniformMatrix4fv(r.rectLocProj,false,new Float32Array(16));gl.uniform4f(r.rectLocColor,1,1,1,1);gl.drawArrays(gl.TRIANGLE_STRIP,0,4)};
const window={__niconi:{renderer:r}};
const btoa=s=>Buffer.from(s,'latin1').toString('base64');
vm.runInNewContext(fs.readFileSync(process.argv[1],'utf8'),{window,btoa,Uint8Array,Float32Array,Map,Set,WeakMap,Number,Error,Math,CompressionStream,Response});
r.flush(); const out=window.__nicoSpritesTake();
if(out.frames.length!==1 || out.frames[0].commands.length!==1 || out.frames[0].commands[0].id!==0) throw Error('flush draw was not captured');
const pixels=new Uint8Array([0,0,0,255,255,255,255,255,0,0,0,255,255,255,255,255,0,0,0,255,255,255,255,255,0,0,0,255,255,255,255,255]);
if(window.__nicoSpritesPackTextureForTest(pixels,8,1).encoding!=='palette8') throw Error('packing is not enabled by default');
(async()=>{ window.__nicoSpritesDeflate=true; const tex=r._createTexture({width:32,height:32}); r.flush(); const deflated=await window.__nicoSpritesTake(); if(deflated.textures.length!==1 || deflated.textures[0].compression!=='deflate') throw Error('deflate trial did not compress'); gl.deleteTexture(tex); gl.deleteTexture(tex); r.flush(); const retired=await window.__nicoSpritesTake(); if(retired.deletes.length!==1) throw Error('duplicate texture deletion emitted'); const fixture=new Uint8Array(1024*1024); let x=0x12345678; for(let i=0;i<fixture.length;i++){x^=x<<13;x^=x>>>17;x^=x<<5;fixture[i]=x&255;} const encoded=await window.__nicoSpritesDeflateForTest(fixture); const round=zlib.inflateSync(Buffer.from(encoded,'base64')); if(round.length!==fixture.length || !Buffer.from(round).equals(Buffer.from(fixture))) throw Error('large deflate roundtrip failed'); })().catch(error=>{ console.error(error); process.exitCode=1; });
`
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "-e", script, "internal/nicorender/assets/sprites.js")
	cmd.Dir = "../../"
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("node VM: %v: %s", err, strings.TrimSpace(string(output)))
	}
}
