// Test-only: install after sprite hooks, before first draw. Original RGBA stays intact.
(() => {
  const r=window.__niconi&&window.__niconi.renderer;
  if(!r||r.rendererName!=='WebGL2Renderer'||!window.__nicoSpritesReady)throw Error('sprite hooks required');
  window.__nicoSpritesPack=false;window.__nicoSpritesDeflate=false;window.__nicoSpritesReference=true;
  const C=CanvasRenderingContext2D.prototype,native={};
  for(const n of ['fillText','strokeText','drawImage','clearRect','fillRect','strokeRect','fill','stroke','clip'])native[n]=C[n];
  const shadows=new WeakMap(),info=new Map(),fonts=new Set();
  let nextID=1,sequence=0,fillTextCalls=0,strokeTextCalls=0,fallbackTextures=0;
  const rgba=value=>{
    const text=String(value).toLowerCase();let m=text.match(/^#([0-9a-f]{6})([0-9a-f]{2})?$/);
    if(m){const n=parseInt(m[1],16),a=m[2]?parseInt(m[2],16):255;return ((n>>16)|((n>>8&255)<<8)|((n&255)<<16)|(a<<24))>>>0;}
    m=text.match(/^rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)(?:[,\s/]+([\d.]+))?\s*\)$/);
    if(m)return (Number(m[1])|(Number(m[2])<<8)|(Number(m[3])<<16)|(Math.round((m[4]===undefined?1:Number(m[4]))*255)<<24))>>>0;
    throw Error('unsupported paint '+text);
  };
  const state=ctx=>{let s=shadows.get(ctx.canvas);if(!s){const canvas=document.createElement('canvas');canvas.width=ctx.canvas.width;canvas.height=ctx.canvas.height;s={canvas,ctx:canvas.getContext('2d'),fill:null,outline:null,radius:0,unsupported:false};shadows.set(ctx.canvas,s);}return s;};
  const sync=(from,to)=>{
    to.setTransform(from.getTransform());
    for(const k of ['font','textAlign','textBaseline','direction','fontKerning','fontStretch','fontVariantCaps','letterSpacing','wordSpacing','textRendering','imageSmoothingEnabled','imageSmoothingQuality'])if(k in from)to[k]=from[k];
    to.globalAlpha=1;to.globalCompositeOperation='source-over';to.filter='none';to.fillStyle='#ffffff';
  };
  const remember=(ctx,s)=>{
    const fill=rgba(ctx.fillStyle),outline=rgba(ctx.strokeStyle),tr=ctx.getTransform(),radius=ctx.lineWidth*.5*Math.max(Math.abs(tr.a),Math.abs(tr.d));
    if(s.fill!==null&&(s.fill!==fill||s.outline!==outline||Math.abs(s.radius-radius)>.001))s.unsupported=true;
    if(ctx.globalAlpha!==1||ctx.globalCompositeOperation!=='source-over'||ctx.filter!=='none'||ctx.shadowBlur!==0||tr.b!==0||tr.c!==0)s.unsupported=true;
    s.fill=fill;s.outline=outline;s.radius=radius;fonts.add(ctx.font);
  };
  C.strokeText=function(...a){strokeTextCalls++;return native.strokeText.apply(this,a);};
  C.fillText=function(...a){fillTextCalls++;const s=state(this);remember(this,s);sync(this,s.ctx);native.fillText.apply(s.ctx,a);return native.fillText.apply(this,a);};
  C.drawImage=function(source,...a){const ss=shadows.get(source);if(ss){const s=state(this);sync(this,s.ctx);native.drawImage.call(s.ctx,ss.canvas,...a);if(s.fill!==null&&(s.fill!==ss.fill||s.outline!==ss.outline))s.unsupported=true;s.fill=ss.fill;s.outline=ss.outline;s.radius=ss.radius;s.unsupported||=ss.unsupported;}return native.drawImage.call(this,source,...a);};
  C.clearRect=function(...a){const s=shadows.get(this.canvas);if(s){sync(this,s.ctx);native.clearRect.apply(s.ctx,a);}return native.clearRect.apply(this,a);};
  for(const n of ['fillRect','strokeRect','fill','stroke','clip'])C[n]=function(...a){state(this).unsupported=true;return native[n].apply(this,a);};
  for(const n of ['width','height']){const d=Object.getOwnPropertyDescriptor(HTMLCanvasElement.prototype,n);Object.defineProperty(HTMLCanvasElement.prototype,n,{...d,set(v){d.set.call(this,v);shadows.delete(this);}});}
  const encode=bytes=>{let s='';for(let i=0;i<bytes.length;i+=16384)s+=String.fromCharCode(...bytes.subarray(i,i+16384));return btoa(s);};
  const create=r._createTexture.bind(r);
  r._createTexture=source=>{
    const texture=create(source),id=nextID++,s=shadows.get(source),mask=new Uint8Array(source.width*source.height);let fallback=!s||s.unsupported||s.fill===null;
    if(s){const data=s.ctx.getImageData(0,0,source.width,source.height).data;for(let i=0;i<mask.length;i++){const o=i*4;mask[i]=data[o+3];if(data[o+3]&&(data[o]!==255||data[o+1]!==255||data[o+2]!==255))fallback=true;}}
    if(!fallback&&!mask.some(a=>a>0))fallback=true;if(fallback)fallbackTextures++;
    info.set(id,{fill:encode(mask),fillRGBA:s&&s.fill!==null?s.fill:0,outlineRGBA:s&&s.outline!==null?s.outline:0,outlineRadiusPx:s?s.radius:0,fallback});return texture;
  };
  window.__nicoMaskTake=()=>{const b=window.__nicoSpritesTake();return {textures:b.textures.map(t=>{if(t.encoding||t.compression)throw Error('raw sprite reference required');const e=info.get(t.id);if(!e)throw Error('texture identity mismatch');info.delete(t.id);return {...e,id:t.id,width:t.width,height:t.height,rgba:t.data};}),frames:b.frames.map(f=>{const n=sequence++;return {sequence:n,timeMs:Math.floor(n*1000/60),commands:f.commands};})};};
  window.__nicoMaskMetadata=()=>({strokeTextCalls,fillTextCalls,strokeTextSuppressed:true,fonts:Array.from(fonts),fallbackTextures});
  window.__nicoMaskReady=true;
})();
