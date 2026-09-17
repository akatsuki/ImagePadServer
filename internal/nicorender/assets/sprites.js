// Production sprite capture for niconicomments' WebGL2 renderer.
// The source canvas is still rendered by niconicomments; this records the
// exact texture readback and draw order consumed by the native compositor.
(() => {
  const r = window.__niconi && window.__niconi.renderer;
  if (!r || r.rendererName !== 'WebGL2Renderer') throw Error('WebGL2 renderer required');
  const gl = r.gl;
  const textureIDs = new WeakMap();
  const pendingRetireIDs = new Set();
  const uniforms = new Map();
  let nextTextureID = 1;
  let boundTexture = null;
  let program = null;
  let commands = [];
  let frames = [];
  let textures = [];

  const originalBindTexture = gl.bindTexture.bind(gl);
  const originalUseProgram = gl.useProgram.bind(gl);
  const originalUniform4f = gl.uniform4f.bind(gl);
  const originalUniform1f = gl.uniform1f.bind(gl);
  const originalUniformMatrix4fv = gl.uniformMatrix4fv.bind(gl);
  const originalDrawArrays = gl.drawArrays.bind(gl);
  const originalDeleteTexture = gl.deleteTexture.bind(gl);
  const originalCreateTexture = r._createTexture.bind(r);
  const originalFlush = r.flush.bind(r);

  const encodeBytes = bytes => {
    let text = '';
    for (let i = 0; i < bytes.length; i += 16384) {
      const end = Math.min(i + 16384, bytes.length);
      text += String.fromCharCode(...bytes.subarray(i, end));
    }
    return btoa(text);
  };

  const packTexture = (pixels, width, height, force = false) => {
    if (!force && !window.__nicoSpritesPack) return { data: pixels, encoding: '', palette: new Uint8Array(0) };
    const count = width * height;
    const seen = new Map();
    const entries = [];
    const indexes = new Uint8Array(count);
    let isGray = true;
    let paletteComplete = true;
    for (let p = 0; p < count; p++) {
      const o = p * 4;
      const red = pixels[o], green = pixels[o + 1], blue = pixels[o + 2], alpha = pixels[o + 3];
      if (red !== green || red !== blue) isGray = false;
      if (!paletteComplete) continue;
      const key = (((red << 24) >>> 0) | (green << 16) | (blue << 8) | alpha) >>> 0;
      let index = seen.get(key);
      if (index === undefined) {
        if (entries.length >= 256) {
          paletteComplete = false;
        } else {
          index = entries.length;
          seen.set(key, index);
          entries.push([red, green, blue, alpha]);
        }
      }
      if (index !== undefined) indexes[p] = index;
    }
    const rawBytes = pixels.length;
    const paletteBytes = entries.length * 4 + indexes.length;
    if (paletteComplete && entries.length > 0 && paletteBytes < rawBytes) {
      const palette = new Uint8Array(entries.length * 4);
      for (let i = 0; i < entries.length; i++) palette.set(entries[i], i * 4);
      return { data: indexes, encoding: 'palette8', palette };
    }
    if (isGray && count * 2 < rawBytes) {
      const grayAlpha = new Uint8Array(count * 2);
      for (let p = 0; p < count; p++) {
        grayAlpha[p * 2] = pixels[p * 4];
        grayAlpha[p * 2 + 1] = pixels[p * 4 + 3];
      }
      return { data: grayAlpha, encoding: 'grayalpha8', palette: new Uint8Array(0) };
    }
    return { data: pixels, encoding: '', palette: new Uint8Array(0) };
  };

  window.__nicoSpritesPackTextureForTest = (pixels, width, height) => {
    const packed = packTexture(pixels, width, height, true);
    return { data: encodeBytes(packed.data), encoding: packed.encoding, palette: encodeBytes(packed.palette) };
  };
  window.__nicoSpritesPack = true;

  gl.bindTexture = (target, texture) => {
    if (target === gl.TEXTURE_2D) boundTexture = texture;
    return originalBindTexture(target, texture);
  };
  gl.useProgram = value => {
    program = value;
    return originalUseProgram(value);
  };
  gl.uniform4f = (location, ...values) => {
    uniforms.set(location, values);
    return originalUniform4f(location, ...values);
  };
  gl.uniform1f = (location, value) => {
    uniforms.set(location, [value]);
    return originalUniform1f(location, value);
  };
  gl.uniformMatrix4fv = (location, transpose, value) => {
    uniforms.set(location, Array.from(value));
    return originalUniformMatrix4fv(location, transpose, value);
  };

  r._createTexture = source => {
    const texture = originalCreateTexture(source);
    const width = Number(source && source.width);
    const height = Number(source && source.height);
    if (!Number.isInteger(width) || !Number.isInteger(height) || width <= 0 || height <= 0 || width > 16384 || height > 16384 || width * height * 4 > 128 * 1024 * 1024) {
      throw Error('texture dimensions outside production bound');
    }
    const previousFramebuffer = gl.getParameter(gl.FRAMEBUFFER_BINDING);
    const framebuffer = gl.createFramebuffer();
    gl.bindFramebuffer(gl.FRAMEBUFFER, framebuffer);
    gl.framebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, texture, 0);
    if (gl.checkFramebufferStatus(gl.FRAMEBUFFER) !== gl.FRAMEBUFFER_COMPLETE) throw Error('unreadable texture');
    const pixels = new Uint8Array(width * height * 4);
    gl.readPixels(0, 0, width, height, gl.RGBA, gl.UNSIGNED_BYTE, pixels);
    gl.bindFramebuffer(gl.FRAMEBUFFER, previousFramebuffer);
    gl.deleteFramebuffer(framebuffer);
    const packed = packTexture(pixels, width, height);
    if (nextTextureID === 0 || nextTextureID > 0xffffffff) throw Error('texture ID exhausted');
    const id = nextTextureID++;
    textureIDs.set(texture, id);
    textures.push({ id, width, height, dataBytes: packed.data, encoding: packed.encoding, paletteBytes: packed.palette });
    return texture;
  };

  gl.deleteTexture = texture => {
    const id = texture ? textureIDs.get(texture) : undefined;
    if (id !== undefined) {
      textureIDs.delete(texture);
      pendingRetireIDs.add(id);
    }
    return originalDeleteTexture(texture);
  };

  const uniform = (location, size, name) => {
    const value = uniforms.get(location);
    if (!value || value.length !== size) throw Error(`missing ${name} uniform`);
    return value;
  };
  gl.drawArrays = (mode, first, count) => {
    if (mode !== gl.TRIANGLE_STRIP || first !== 0 || count !== 4) throw Error('unsupported draw primitive');
    if (program === r.spriteProg) {
      const id = textureIDs.get(boundTexture);
      if (id === undefined) throw Error('unregistered texture');
      commands.push({ id, rect: uniform(r.spriteLocRect, 4, 'sprite rect'), proj: uniform(r.spriteLocProj, 16, 'sprite projection'), color: [uniform(r.spriteLocAlpha, 1, 'sprite alpha')[0], 0, 0, 0] });
    } else if (program === r.rectProg) {
      commands.push({ id: 0, rect: uniform(r.rectLocRect, 4, 'rect rect'), proj: uniform(r.rectLocProj, 16, 'rect projection'), color: uniform(r.rectLocColor, 4, 'rect color') });
    } else {
      throw Error('unsupported shader');
    }
    // The native path consumes command data; the original draw preserves the
    // browser reference frame when a diagnostic caller requests it.
    if (window.__nicoSpritesReference) return originalDrawArrays(mode, first, count);
  };

  r.flush = () => {
    commands = [];
    originalFlush();
    frames.push({ commands });
    commands = [];
  };

  const compressDeflate = async data => {
    if (typeof CompressionStream !== 'function' || typeof Response !== 'function') return null;
    const stream = new CompressionStream('deflate');
    const writer = stream.writable.getWriter();
    const output = new Response(stream.readable).arrayBuffer();
    await writer.write(data);
    await writer.close();
    return new Uint8Array(await output);
  };

  const encodedTexture = texture => ({
    id: texture.id,
    width: texture.width,
    height: texture.height,
    data: encodeBytes(texture.dataBytes),
    encoding: texture.encoding,
    palette: encodeBytes(texture.paletteBytes),
    compression: ''
  });
  const takeRaw = () => {
    const result = { textures: textures.map(encodedTexture), frames, deletes: Array.from(pendingRetireIDs) };
    textures = [];
    frames = [];
    pendingRetireIDs.clear();
    return result;
  };
  const takeDeflated = async () => {
    const emitted = [];
    for (const texture of textures) {
      let data = texture.dataBytes;
      let compression = '';
      if (data.length >= 1024) {
        const compressed = await compressDeflate(data);
        if (compressed && compressed.length < data.length) {
          data = compressed;
          compression = 'deflate';
        }
      }
      emitted.push({ id: texture.id, width: texture.width, height: texture.height, data: encodeBytes(data), encoding: texture.encoding, palette: encodeBytes(texture.paletteBytes), compression });
    }
    const result = { textures: emitted, frames, deletes: Array.from(pendingRetireIDs) };
    textures = [];
    frames = [];
    pendingRetireIDs.clear();
    return result;
  };

  window.__nicoSpritesDraw = times => {
    if (!Array.isArray(times)) throw Error('sprite times must be an array');
    for (const timeMs of times) {
      if (!Number.isSafeInteger(timeMs) || timeMs < 0) throw Error('invalid sprite time');
      window.__niconi.drawCanvas(Math.floor(timeMs / 10), true);
    }
  };
  window.__nicoSpritesTake = () => window.__nicoSpritesDeflate ? takeDeflated() : takeRaw();
  window.__nicoSpritesDeflateForTest = async bytes => {
    const compressed = await compressDeflate(bytes);
    return compressed ? encodeBytes(compressed) : '';
  };
  window.__nicoSpritesDeflate = false;
  window.__nicoSpritesReady = true;
})();
