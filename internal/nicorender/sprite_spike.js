// Throwaway probe: record the exact WebGL uniforms and cached texture bytes.
(() => {
  const r=window.__niconi.renderer;
  if(!r||r.rendererName!=='WebGL2Renderer')throw Error('WebGL2 renderer required');
  const gl=r.gl,ids=new Map(),uniforms=new Map();
  let nextID=1,bound=null,prog=null,commands=[],frames=[],textures=[];
  window.__spikeReference=false;
  const bind=gl.bindTexture.bind(gl),use=gl.useProgram.bind(gl),u4=gl.uniform4f.bind(gl),u1=gl.uniform1f.bind(gl),um=gl.uniformMatrix4fv.bind(gl),draw=gl.drawArrays.bind(gl);
  gl.bindTexture=(target,tex)=>{if(target===gl.TEXTURE_2D)bound=tex;return bind(target,tex);};
  gl.useProgram=(p)=>{prog=p;return use(p);};
  gl.uniform4f=(loc,...v)=>{uniforms.set(loc,v);return u4(loc,...v);};
  gl.uniform1f=(loc,v)=>{uniforms.set(loc,v);return u1(loc,v);};
  gl.uniformMatrix4fv=(loc,transpose,v)=>{uniforms.set(loc,Array.from(v));return um(loc,transpose,v);};
  const create=r._createTexture.bind(r);
  const encodeBytes=(bytes)=>{let text='';for(let i=0;i<bytes.length;i+=16384){const end=Math.min(i+16384,bytes.length);for(let j=i;j<end;j++)text+=String.fromCharCode(bytes[j]);}return btoa(text);};
  const packTexture=(pixels,w,h,force=false)=>{
    if(!force&&!window.__spikePack)return {data:encodeBytes(pixels),encoding:'',palette:''};
    const count=w*h,seen=new Map(),entries=[],indexes=new Uint8Array(count);let isGray=true,paletteComplete=true;
    for(let p=0;p<count;p++){
      const o=p*4,r=pixels[o],g=pixels[o+1],b=pixels[o+2],a=pixels[o+3];
      if(r!==g||r!==b)isGray=false;
      if(paletteComplete){
        const key=(((r<<24)>>>0)|((g<<16)>>>0)|((b<<8)>>>0)|a)>>>0;
        let index=seen.get(key);
        if(index===undefined){
          if(entries.length>=256){paletteComplete=false;}else{index=entries.length;seen.set(key,index);entries.push([r,g,b,a]);}
        }
        indexes[p]=index===undefined?0:index;
      }
    }
    const rawBytes=pixels.length,paletteBytes=entries.length*4+indexes.length;
    if(paletteComplete&&entries.length>0&&entries.length<=256&&paletteBytes<rawBytes){
      const palette=new Uint8Array(entries.length*4);for(let i=0;i<entries.length;i++)palette.set(entries[i],i*4);
      return {data:encodeBytes(indexes),encoding:'palette8',palette:encodeBytes(palette)};
    }
    if(isGray&&count*2<rawBytes){const gray=new Uint8Array(count*2);for(let p=0;p<count;p++){gray[p*2]=pixels[p*4];gray[p*2+1]=pixels[p*4+3];}return {data:encodeBytes(gray),encoding:'grayalpha8',palette:''};}
    return {data:encodeBytes(pixels),encoding:'',palette:''};
  };
  window.__spikePack=false;
  window.__spikePackTextureForTest=(pixels,w,h)=>packTexture(pixels,w,h,true);
  r._createTexture=(source)=>{
    const tex=create(source),id=nextID++,w=source.width,h=source.height;
    if(!w||!h||w*h*4>128*1024*1024)throw Error('texture dimensions outside probe bound');
    const old=gl.getParameter(gl.FRAMEBUFFER_BINDING),fb=gl.createFramebuffer();
    gl.bindFramebuffer(gl.FRAMEBUFFER,fb);gl.framebufferTexture2D(gl.FRAMEBUFFER,gl.COLOR_ATTACHMENT0,gl.TEXTURE_2D,tex,0);
    if(gl.checkFramebufferStatus(gl.FRAMEBUFFER)!==gl.FRAMEBUFFER_COMPLETE)throw Error('unreadable texture');
    const pixels=new Uint8Array(w*h*4);gl.readPixels(0,0,w,h,gl.RGBA,gl.UNSIGNED_BYTE,pixels);
    gl.bindFramebuffer(gl.FRAMEBUFFER,old);gl.deleteFramebuffer(fb);
    const packed=packTexture(pixels,w,h);
    ids.set(tex,id);textures.push({id,width:w,height:h,data:packed.data,encoding:packed.encoding,palette:packed.palette});return tex;
  };
  gl.drawArrays=(mode,first,count)=>{
    if(mode!==gl.TRIANGLE_STRIP||first!==0||count!==4)throw Error('unexpected draw primitive');
    if(prog===r.spriteProg){
      if(!ids.has(bound))throw Error('unregistered texture');
      commands.push({id:ids.get(bound),rect:uniforms.get(r.spriteLocRect),proj:uniforms.get(r.spriteLocProj),color:[uniforms.get(r.spriteLocAlpha),0,0,0]});
    }else if(prog===r.rectProg){commands.push({id:0,rect:uniforms.get(r.rectLocRect),proj:uniforms.get(r.rectLocProj),color:uniforms.get(r.rectLocColor)});}
    else throw Error('unexpected shader');
    if(window.__spikeReference)return draw(mode,first,count);
  };
  const flush=r.flush.bind(r);
  r.flush=()=>{commands=[];flush();frames.push({commands});};
  window.__spikeDraw=(sequence,times)=>{for(const t of times)window.__niconi.drawCanvas(Math.floor(t/10),true);};
  window.__spikeTake=()=>{const out={textures,frames};textures=[];frames=[];return out;};
})();
