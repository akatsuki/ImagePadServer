package nicorender

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestSpriteCaptureReportsSynchronousTextureReadbackCost(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	const script = `
const fs=require('fs'), vm=require('vm');
const gl={TEXTURE_2D:1,FRAMEBUFFER:2,FRAMEBUFFER_BINDING:3,COLOR_ATTACHMENT0:4,FRAMEBUFFER_COMPLETE:5,TRIANGLE_STRIP:6,RGBA:7,UNSIGNED_BYTE:8,
  bindTexture(){},useProgram(){},uniform4f(){},uniform1f(){},uniformMatrix4fv(){},drawArrays(){},deleteTexture(){},getParameter(){return null},createFramebuffer(){return {}},bindFramebuffer(){},framebufferTexture2D(){},checkFramebufferStatus(){return 5},readPixels(){},deleteFramebuffer(){}};
const r={rendererName:'WebGL2Renderer',gl,_createTexture(){return {}},flush(){},spriteProg:{},rectProg:{},spriteLocRect:{},spriteLocProj:{},spriteLocAlpha:{},rectLocRect:{},rectLocProj:{},rectLocColor:{}};
const window={__niconi:{renderer:r}};
const btoa=s=>Buffer.from(s,'latin1').toString('base64');
let tick=0;
vm.runInNewContext(fs.readFileSync(process.argv[1],'utf8'),{window,btoa,Uint8Array,Float32Array,Map,Set,WeakMap,Number,Error,Math,Date,CompressionStream,Response,performance:{now:()=>++tick}});
if(typeof window.__nicoSpritesStats!=='function') throw Error('sprite metrics API missing');
r._createTexture({width:8,height:4});
r.flush();
const stats=window.__nicoSpritesStats();
if(stats.textureCreations!==1 || stats.readbackCalls!==1 || stats.readbackBytes!==128) throw Error('synchronous readback count mismatch: '+JSON.stringify(stats));
if(!Number.isFinite(stats.readbackWallMs) || !Number.isFinite(stats.packWallMs) || !Number.isFinite(stats.drawWallMs)) throw Error('sprite stage timing is missing');
`
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "-e", script, "internal/nicorender/assets/sprites.js")
	cmd.Dir = "../../"
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("node VM: %v: %s", err, strings.TrimSpace(string(output)))
	}
}

func TestSpritePBOCapacityFallbackStillFinalizesTimelineTexture(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	const script = `
const fs=require('fs'), vm=require('vm');
const state=new Map();
const gl={TEXTURE_2D:1,FRAMEBUFFER:2,FRAMEBUFFER_BINDING:3,COLOR_ATTACHMENT0:4,FRAMEBUFFER_COMPLETE:5,TRIANGLE_STRIP:6,RGBA:7,UNSIGNED_BYTE:8,
  READ_FRAMEBUFFER:9,READ_FRAMEBUFFER_BINDING:10,READ_BUFFER:11,PIXEL_PACK_BUFFER:12,PIXEL_PACK_BUFFER_BINDING:13,
  PACK_ALIGNMENT:14,PACK_ROW_LENGTH:15,PACK_SKIP_PIXELS:16,PACK_SKIP_ROWS:17,
  bindTexture(){},useProgram(){},uniform4f(){},uniform1f(){},uniformMatrix4fv(){},drawArrays(){},deleteTexture(){},getParameter(key){return state.get(key)??null},createFramebuffer(){return {kind:'new-framebuffer'}},
  bindFramebuffer(_target,value){state.set(this.READ_FRAMEBUFFER_BINDING,value)},readBuffer(value){state.set(this.READ_BUFFER,value)},
  bindBuffer(_target,value){state.set(this.PIXEL_PACK_BUFFER_BINDING,value)},pixelStorei(key,value){state.set(key,value)},
  framebufferTexture2D(){},checkFramebufferStatus(){return 5},readPixels(_x,_y,_w,_h,_format,_type,pixels){if(state.get(this.PIXEL_PACK_BUFFER_BINDING)!==null) throw Error('sync readback left PBO bound'); pixels.set([1,2,3,4,1,2,3,4])},deleteFramebuffer(){}};
const callerState={framebuffer:{kind:'caller-framebuffer'},pixelPack:{kind:'caller-pbo'},readBuffer:99,alignment:8,rowLength:13,skipPixels:2,skipRows:3};
state.set(gl.READ_FRAMEBUFFER_BINDING,callerState.framebuffer);state.set(gl.PIXEL_PACK_BUFFER_BINDING,callerState.pixelPack);state.set(gl.READ_BUFFER,callerState.readBuffer);
state.set(gl.PACK_ALIGNMENT,callerState.alignment);state.set(gl.PACK_ROW_LENGTH,callerState.rowLength);state.set(gl.PACK_SKIP_PIXELS,callerState.skipPixels);state.set(gl.PACK_SKIP_ROWS,callerState.skipRows);
const r={rendererName:'WebGL2Renderer',gl,_createTexture(){return {}},flush(){},spriteProg:{},rectProg:{},spriteLocRect:{},spriteLocProj:{},spriteLocAlpha:{},rectLocRect:{},rectLocProj:{},rectLocColor:{}};
const window={__niconi:{renderer:r},__nicoTimelinePBO:{available:true,enqueue(){return null},drain:async()=>{},getStats:()=>({transferBytes:0,drainCalls:0,fenceWaitWallMs:0,fallbackCount:1,fallbackReason:'capacity exhausted',peakBytes:16})}};
const btoa=s=>Buffer.from(s,'latin1').toString('base64');
vm.runInNewContext(fs.readFileSync(process.argv[1],'utf8'),{window,btoa,Uint8Array,Float32Array,Map,Set,WeakMap,Number,Error,Math,Date,CompressionStream:undefined,Response:undefined,performance:{now:()=>1}});
const texture=r._createTexture({width:2,height:1});
if(!texture) throw Error('sync fallback did not return the texture');
const batch=window.__nicoSpritesTakeForTimeline();
if(!batch.deferredPBO || batch.textures.length!==1 || !(batch.textures[0].dataBytes instanceof Uint8Array)) throw Error('fallback texture was not retained for deferred timeline serialization');
if(state.get(gl.READ_FRAMEBUFFER_BINDING)!==callerState.framebuffer || state.get(gl.PIXEL_PACK_BUFFER_BINDING)!==callerState.pixelPack || state.get(gl.READ_BUFFER)!==callerState.readBuffer ||
  state.get(gl.PACK_ALIGNMENT)!==callerState.alignment || state.get(gl.PACK_ROW_LENGTH)!==callerState.rowLength || state.get(gl.PACK_SKIP_PIXELS)!==callerState.skipPixels || state.get(gl.PACK_SKIP_ROWS)!==callerState.skipRows) throw Error('sync fallback did not restore WebGL2 state');
const element={deferredPBO:true,samples:[{textures:batch.textures}]};
if(window.__nicoSpritesTimelinePendingPixelBytes([element])!==6) throw Error('pending pixel bytes were not counted');
window.__nicoSpritesRecordTimelinePendingPixelBytes(6);
window.__nicoSpritesFinalizeTimelineElement(element).then(result=>{
  const emitted=result.samples[0].textures[0];
  if(emitted.encoding!=='palette8' || emitted.data!=='AAA=' || emitted.palette!=='AQIDBA==' || emitted.compression!=='') throw Error('fallback texture serialization mismatch: '+JSON.stringify(emitted));
  if(result.deferredPBO!==undefined || result.spriteMetrics.mode!=='sync' || result.spriteMetrics.pboFallbacks!==1 || result.spriteMetrics.pendingPixelPeakBytes!==6) throw Error('fallback diagnostics are missing: '+JSON.stringify(result.spriteMetrics));
}).catch(error=>{console.error(error);process.exitCode=1});
`
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "-e", script, "internal/nicorender/assets/sprites.js")
	cmd.Dir = "../../"
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("node VM: %v: %s", err, strings.TrimSpace(string(output)))
	}
}
