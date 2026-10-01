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
  let forwardingGLState = false;
  let commands = [];
  let frames = [];
  let textures = [];
  const captureMetrics = {
    textureCreations: 0,
    drawWallMs: 0,
    readbackCalls: 0,
    readbackBytes: 0,
    readbackWallMs: 0,
    pboEnqueueWallMs: 0,
    pboDrainWallMs: 0,
    pendingPixelPeakBytes: 0,
    packWallMs: 0,
    deflateWallMs: 0,
    serializeWallMs: 0
  };
  const now = () => typeof performance !== 'undefined' && typeof performance.now === 'function' ? performance.now() : Date.now();
  const metricSnapshot = () => {
    const pbo = window.__nicoTimelinePBO && window.__nicoTimelinePBO.getStats
      ? window.__nicoTimelinePBO.getStats()
      : null;
    return {
      ...captureMetrics,
      mode: pbo && pbo.transferBytes > 0 ? 'pbo' : 'sync',
      pboTextures: captureMetrics.pboTextures || 0,
      pboDrainCalls: pbo ? pbo.drainCalls : 0,
      pboFenceWaitWallMs: pbo ? pbo.fenceWaitWallMs : 0,
      pboCopyWallMs: pbo ? pbo.copyWallMs : 0,
      pboBytes: pbo ? pbo.transferBytes : 0,
      pboFallbacks: pbo ? pbo.fallbackCount : 0,
      pboFallbackReason: pbo ? pbo.fallbackReason : '',
      pboPeakBytes: pbo ? pbo.peakBytes : 0
    };
  };

  const originalBindTexture = gl.bindTexture.bind(gl);
  const originalUseProgram = gl.useProgram.bind(gl);
  const originalUniform4f = gl.uniform4f.bind(gl);
  const originalUniform1f = gl.uniform1f.bind(gl);
  const originalUniformMatrix4fv = gl.uniformMatrix4fv.bind(gl);
  const originalDrawArrays = gl.drawArrays.bind(gl);
  const originalDeleteTexture = gl.deleteTexture.bind(gl);
  const originalCreateTexture = r._createTexture.bind(r);
  const originalFlush = r.flush.bind(r);

  const legacyEncodeBytes = bytes => {
    let text = '';
    for (let i = 0; i < bytes.length; i += 16384) {
      const end = Math.min(i + 16384, bytes.length);
      text += String.fromCharCode(...bytes.subarray(i, end));
    }
    return btoa(text);
  };

  const encodeBytes = (bytes, testMode) => {
    const started = now();
    let encoded;
    try {
      const nativeToBase64 = Uint8Array.prototype.toBase64;
      encoded = testMode !== 'legacy' && typeof nativeToBase64 === 'function'
        ? nativeToBase64.call(bytes)
        : legacyEncodeBytes(bytes);
    } finally {
      captureMetrics.serializeWallMs += now() - started;
    }
    return encoded;
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
  window.__nicoSpritesEncodeBytesForTest = (bytes, mode) => {
    if (mode !== undefined && mode !== 'native' && mode !== 'legacy') throw Error('invalid Base64 test mode');
    return encodeBytes(bytes, mode);
  };
  window.__nicoSpritesPack = true;

  gl.bindTexture = (target, texture) => {
    if (target === gl.TEXTURE_2D) boundTexture = texture;
    if (forwardingGLState || window.__nicoSpritesReference) return originalBindTexture(target, texture);
  };
  gl.useProgram = value => {
    program = value;
    if (forwardingGLState || window.__nicoSpritesReference) return originalUseProgram(value);
  };
  gl.uniform4f = (location, ...values) => {
    uniforms.set(location, values);
    if (forwardingGLState || window.__nicoSpritesReference) return originalUniform4f(location, ...values);
  };
  gl.uniform1f = (location, value) => {
    uniforms.set(location, [value]);
    if (forwardingGLState || window.__nicoSpritesReference) return originalUniform1f(location, value);
  };
  gl.uniformMatrix4fv = (location, transpose, value) => {
    uniforms.set(location, Array.from(value));
    if (forwardingGLState || window.__nicoSpritesReference) return originalUniformMatrix4fv(location, transpose, value);
  };

  r._createTexture = source => {
    forwardingGLState = true;
    let texture;
    try {
      texture = originalCreateTexture(source);
    } finally {
      forwardingGLState = false;
    }
    const width = Number(source && source.width);
    const height = Number(source && source.height);
    if (!Number.isInteger(width) || !Number.isInteger(height) || width <= 0 || height <= 0 || width > 16384 || height > 16384 || width * height * 4 > 128 * 1024 * 1024) {
      throw Error('texture dimensions outside production bound');
    }
    if (nextTextureID === 0 || nextTextureID > 0xffffffff) throw Error('texture ID exhausted');
    const id = nextTextureID++;
    textureIDs.set(texture, id);
    captureMetrics.textureCreations++;
    const pbo = window.__nicoTimelinePBO;
    if (pbo) {
      const enqueueStarted = now();
      let descriptor;
      try {
        descriptor = pbo.enqueue(texture, width, height, id);
      } finally {
        captureMetrics.pboEnqueueWallMs += now() - enqueueStarted;
      }
      if (descriptor) {
        captureMetrics.pboTextures = (captureMetrics.pboTextures || 0) + 1;
        captureMetrics.readbackCalls++;
        captureMetrics.readbackBytes += descriptor.byteLength;
        textures.push({ id, width, height, pboDescriptor: descriptor, needsPack: true });
        return texture;
      }
    }
    const useReadTarget = !!pbo && gl.READ_FRAMEBUFFER !== undefined && gl.READ_FRAMEBUFFER_BINDING !== undefined;
    const framebufferTarget = useReadTarget ? gl.READ_FRAMEBUFFER : gl.FRAMEBUFFER;
    const previousFramebuffer = gl.getParameter(useReadTarget ? gl.READ_FRAMEBUFFER_BINDING : gl.FRAMEBUFFER_BINDING);
    const previousReadBuffer = useReadTarget && gl.READ_BUFFER !== undefined ? gl.getParameter(gl.READ_BUFFER) : undefined;
    const canRestorePixelPack = !!pbo && gl.PIXEL_PACK_BUFFER_BINDING !== undefined && typeof gl.bindBuffer === 'function';
    const previousPixelPackBuffer = canRestorePixelPack ? gl.getParameter(gl.PIXEL_PACK_BUFFER_BINDING) : undefined;
    const canSetPackState = !!pbo && typeof gl.pixelStorei === 'function';
    const previousPackState = canSetPackState ? [
      [gl.PACK_ALIGNMENT, gl.getParameter(gl.PACK_ALIGNMENT)],
      [gl.PACK_ROW_LENGTH, gl.getParameter(gl.PACK_ROW_LENGTH)],
      [gl.PACK_SKIP_PIXELS, gl.getParameter(gl.PACK_SKIP_PIXELS)],
      [gl.PACK_SKIP_ROWS, gl.getParameter(gl.PACK_SKIP_ROWS)]
    ].filter(([key]) => key !== undefined) : [];
    const framebuffer = gl.createFramebuffer();
    if (!framebuffer) throw Error('unreadable texture framebuffer allocation failed');
    try {
      gl.bindFramebuffer(framebufferTarget, framebuffer);
      gl.framebufferTexture2D(framebufferTarget, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, texture, 0);
      if (gl.checkFramebufferStatus(framebufferTarget) !== gl.FRAMEBUFFER_COMPLETE) throw Error('unreadable texture');
      if (useReadTarget && typeof gl.readBuffer === 'function') gl.readBuffer(gl.COLOR_ATTACHMENT0);
      if (canRestorePixelPack) gl.bindBuffer(gl.PIXEL_PACK_BUFFER, null);
      if (canSetPackState) {
        gl.pixelStorei(gl.PACK_ALIGNMENT, 1);
        gl.pixelStorei(gl.PACK_ROW_LENGTH, 0);
        gl.pixelStorei(gl.PACK_SKIP_PIXELS, 0);
        gl.pixelStorei(gl.PACK_SKIP_ROWS, 0);
      }
      const pixels = new Uint8Array(width * height * 4);
      const readbackStarted = now();
      try {
        gl.readPixels(0, 0, width, height, gl.RGBA, gl.UNSIGNED_BYTE, pixels);
      } finally {
        captureMetrics.readbackCalls++;
        captureMetrics.readbackBytes += pixels.byteLength;
        captureMetrics.readbackWallMs += now() - readbackStarted;
      }
      const packStarted = now();
      const packed = packTexture(pixels, width, height);
      captureMetrics.packWallMs += now() - packStarted;
      textures.push({ id, width, height, dataBytes: packed.data, encoding: packed.encoding, paletteBytes: packed.palette });
    } finally {
      gl.bindFramebuffer(framebufferTarget, previousFramebuffer);
      if (previousReadBuffer !== undefined && typeof gl.readBuffer === 'function') gl.readBuffer(previousReadBuffer);
      if (canRestorePixelPack) gl.bindBuffer(gl.PIXEL_PACK_BUFFER, previousPixelPackBuffer);
      for (const [key, value] of previousPackState) {
        if (key !== undefined && value !== undefined) gl.pixelStorei(key, value);
      }
      gl.deleteFramebuffer(framebuffer);
    }
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
    const drawStarted = now();
    try {
      originalFlush();
    } finally {
      captureMetrics.drawWallMs += now() - drawStarted;
    }
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
    const result = { textures: textures.map(encodedTexture), frames, deletes: Array.from(pendingRetireIDs), metrics: metricSnapshot() };
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
        const deflateStarted = now();
        const compressed = await compressDeflate(data);
        captureMetrics.deflateWallMs += now() - deflateStarted;
        if (compressed && compressed.length < data.length) {
          data = compressed;
          compression = 'deflate';
        }
      }
      emitted.push({ id: texture.id, width: texture.width, height: texture.height, data: encodeBytes(data), encoding: texture.encoding, palette: encodeBytes(texture.paletteBytes), compression });
    }
    const result = { textures: emitted, frames, deletes: Array.from(pendingRetireIDs), metrics: metricSnapshot() };
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

  const takeForTimeline = () => {
    const pbo = window.__nicoTimelinePBO;
    if (!pbo || !pbo.available) return window.__nicoSpritesTake();
    const result = { textures, frames, deletes: Array.from(pendingRetireIDs), deferredPBO: true };
    textures = [];
    frames = [];
    pendingRetireIDs.clear();
    return result;
  };

  const estimateEncodedTextureLength = texture => {
    const dataLength = texture.pboDescriptor ? texture.pboDescriptor.byteLength : texture.dataBytes.length;
    const paletteLength = texture.paletteBytes ? texture.paletteBytes.length : 0;
    const encodedLength = length => Math.ceil(length / 3) * 4;
    const shell = JSON.stringify({
      id: texture.id, width: texture.width, height: texture.height,
      data: '', encoding: texture.encoding || '', palette: '', compression: ''
    });
    return shell.length + encodedLength(dataLength) + encodedLength(paletteLength);
  };

  const estimateTimelineElementLength = element => {
    const texturesInElement = [];
    const base = {
      ...element,
      samples: element.samples.map(sample => {
        texturesInElement.push(...(sample.textures || []));
        return { ...sample, textures: [] };
      })
    };
    let estimate = JSON.stringify(base).length;
    for (const texture of texturesInElement) estimate += estimateEncodedTextureLength(texture) + 1;
    // Final cumulative counters can add a few digits after the batch drains.
    return estimate + 256;
  };

  const timelinePendingPixelBytes = elements => {
    let bytes = 0;
    for (const element of elements || []) {
      for (const sample of element.samples || []) {
        for (const texture of sample.textures || []) {
          if (texture.pboDescriptor) bytes += texture.pboDescriptor.byteLength;
          else {
            if (texture.dataBytes && Number.isSafeInteger(texture.dataBytes.byteLength)) bytes += texture.dataBytes.byteLength;
            if (texture.paletteBytes && Number.isSafeInteger(texture.paletteBytes.byteLength)) bytes += texture.paletteBytes.byteLength;
          }
        }
      }
    }
    return bytes;
  };

  const recordTimelinePendingPixelBytes = bytes => {
    if (!Number.isSafeInteger(bytes) || bytes < 0) throw Error('invalid pending timeline pixel byte count');
    captureMetrics.pendingPixelPeakBytes = Math.max(captureMetrics.pendingPixelPeakBytes, bytes);
  };

  const finalizeTimelineElement = async element => {
    for (const sample of element.samples || []) {
      const finalized = [];
      for (const texture of sample.textures || []) {
        let data = texture.dataBytes;
        let encoding = texture.encoding || '';
        let palette = texture.paletteBytes || new Uint8Array(0);
        if (texture.pboDescriptor) {
          if (!texture.pboDescriptor.pixels) throw Error(`PBO pixels missing for texture ${texture.id}`);
          const packStarted = now();
          const packed = packTexture(texture.pboDescriptor.pixels, texture.width, texture.height);
          captureMetrics.packWallMs += now() - packStarted;
          data = packed.data;
          encoding = packed.encoding;
          palette = packed.palette;
        }
        let compression = '';
        if (window.__nicoSpritesDeflate && data.length >= 1024) {
          const deflateStarted = now();
          const compressed = await compressDeflate(data);
          captureMetrics.deflateWallMs += now() - deflateStarted;
          if (compressed && compressed.length < data.length) {
            data = compressed;
            compression = 'deflate';
          }
        }
        finalized.push({
          id: texture.id, width: texture.width, height: texture.height,
          data: encodeBytes(data), encoding, palette: encodeBytes(palette), compression
        });
      }
      sample.textures = finalized;
    }
    delete element.deferredPBO;
    element.spriteMetrics = metricSnapshot();
    return element;
  };

  const drainTimelinePBO = async () => {
    const pbo = window.__nicoTimelinePBO;
    if (!pbo || !pbo.available) return;
    const started = now();
    await pbo.drain();
    captureMetrics.pboDrainWallMs += now() - started;
  };
  window.__nicoSpritesStats = metricSnapshot;
  window.__nicoSpritesTake = () => window.__nicoSpritesDeflate ? takeDeflated() : takeRaw();
  window.__nicoSpritesTakeForTimeline = takeForTimeline;
  window.__nicoSpritesEstimateTimelineElementLength = estimateTimelineElementLength;
  window.__nicoSpritesTimelinePendingPixelBytes = timelinePendingPixelBytes;
  window.__nicoSpritesRecordTimelinePendingPixelBytes = recordTimelinePendingPixelBytes;
  window.__nicoSpritesFinalizeTimelineElement = finalizeTimelineElement;
  window.__nicoSpritesDrainTimelinePBO = drainTimelinePBO;
  window.__nicoSpritesDeflateForTest = async bytes => {
    const compressed = await compressDeflate(bytes);
    return compressed ? encodeBytes(compressed) : '';
  };
  window.__nicoSpritesDeflate = false;
  window.__nicoSpritesReady = true;
})();
