// Bounded WebGL2 pixel-pack-buffer readback for timeline sprite capture.
// The caller remains responsible for packing and serializing returned pixels.
(() => {
  const renderer = window.__niconi && window.__niconi.renderer;
  const gl = renderer && renderer.gl;
  const config = window.__nicoTimelinePBOOptions || {};
  const defaultPageSizes = [1, 2, 4, 8, 16].map(mib => mib * 1024 * 1024);
  const pageSizes = Array.isArray(config.pageSizes)
    ? config.pageSizes.filter(value => Number.isSafeInteger(value) && value > 0).sort((a, b) => a - b)
    : defaultPageSizes;
  const maxLiveBytes = Number.isSafeInteger(config.maxLiveBytes) && config.maxLiveBytes > 0
    ? Math.min(config.maxLiveBytes, 32 * 1024 * 1024)
    : 32 * 1024 * 1024;
  const maxPages = Number.isSafeInteger(config.maxPages) && config.maxPages > 0
    ? Math.min(32, config.maxPages)
    : 32;
  const maxImageBytes = pageSizes.length ? pageSizes[pageSizes.length - 1] : 0;
  const pages = [];
  const stats = {
    liveBytes: 0,
    peakBytes: 0,
    transferBytes: 0,
    fenceWaitWallMs: 0,
    copyWallMs: 0,
    drainCalls: 0,
    fallbackCount: 0,
    fallbackReason: '',
    disposed: false
  };
  let framebuffer = null;
  let available = !!(gl && gl.PIXEL_PACK_BUFFER !== undefined && gl.PIXEL_PACK_BUFFER_BINDING !== undefined &&
    gl.READ_FRAMEBUFFER !== undefined && gl.READ_FRAMEBUFFER_BINDING !== undefined && gl.READ_BUFFER !== undefined &&
    gl.PACK_ALIGNMENT !== undefined && gl.PACK_ROW_LENGTH !== undefined &&
    gl.PACK_SKIP_PIXELS !== undefined && gl.PACK_SKIP_ROWS !== undefined &&
    gl.STREAM_READ !== undefined && gl.RGBA !== undefined && gl.UNSIGNED_BYTE !== undefined &&
    gl.FRAMEBUFFER_COMPLETE !== undefined && gl.COLOR_ATTACHMENT0 !== undefined && gl.TEXTURE_2D !== undefined &&
    gl.SYNC_GPU_COMMANDS_COMPLETE !== undefined && gl.ALREADY_SIGNALED !== undefined &&
    gl.CONDITION_SATISFIED !== undefined && gl.TIMEOUT_EXPIRED !== undefined && gl.WAIT_FAILED !== undefined &&
    typeof gl.getParameter === 'function' && typeof gl.readBuffer === 'function' && typeof gl.bindFramebuffer === 'function' &&
    typeof gl.framebufferTexture2D === 'function' && typeof gl.checkFramebufferStatus === 'function' &&
    typeof gl.createFramebuffer === 'function' && typeof gl.createBuffer === 'function' &&
    typeof gl.bindBuffer === 'function' && typeof gl.bufferData === 'function' &&
    typeof gl.pixelStorei === 'function' && typeof gl.readPixels === 'function' &&
    typeof gl.fenceSync === 'function' && typeof gl.clientWaitSync === 'function' &&
    typeof gl.getBufferSubData === 'function' && typeof gl.flush === 'function' &&
    typeof gl.deleteBuffer === 'function' && typeof gl.deleteSync === 'function');

  const now = () => typeof performance !== 'undefined' && typeof performance.now === 'function' ? performance.now() : Date.now();
  const pendingCount = () => pages.reduce((total, page) => total + page.descriptors.length, 0);
  const fail = reason => { throw new Error(`timeline PBO ${reason}`); };
  const fallback = reason => {
    stats.fallbackCount++;
    stats.fallbackReason = reason;
    return null;
  };

  const saveGLState = () => ({
    framebuffer: gl.getParameter(gl.READ_FRAMEBUFFER_BINDING),
    readBuffer: gl.getParameter(gl.READ_BUFFER),
    pixelPackBuffer: gl.getParameter(gl.PIXEL_PACK_BUFFER_BINDING),
    packAlignment: gl.getParameter(gl.PACK_ALIGNMENT),
    packRowLength: gl.getParameter(gl.PACK_ROW_LENGTH),
    packSkipPixels: gl.getParameter(gl.PACK_SKIP_PIXELS),
    packSkipRows: gl.getParameter(gl.PACK_SKIP_ROWS)
  });

  const restoreGLState = state => {
    gl.bindFramebuffer(gl.READ_FRAMEBUFFER, state.framebuffer);
    if (state.readBuffer !== null && state.readBuffer !== undefined) gl.readBuffer(state.readBuffer);
    gl.bindBuffer(gl.PIXEL_PACK_BUFFER, state.pixelPackBuffer);
    gl.pixelStorei(gl.PACK_ALIGNMENT, state.packAlignment);
    gl.pixelStorei(gl.PACK_ROW_LENGTH, state.packRowLength);
    gl.pixelStorei(gl.PACK_SKIP_PIXELS, state.packSkipPixels);
    gl.pixelStorei(gl.PACK_SKIP_ROWS, state.packSkipRows);
  };

  const ensureFramebuffer = () => {
    if (framebuffer) return true;
    try {
      const created = gl.createFramebuffer();
      if (!created) return false;
      framebuffer = created;
      return true;
    } catch (_) {
      return false;
    }
  };

  const allocatePage = requiredBytes => {
    for (const page of pages) {
      if (!page.fence && page.used === 0 && page.capacity >= requiredBytes) return page;
      if (!page.fence && page.capacity - page.used >= requiredBytes) return page;
    }
    const size = pageSizes.find(candidate => candidate >= requiredBytes);
    if (!size) return fallback(`texture size ${requiredBytes} exceeds the ${maxImageBytes}-byte PBO page limit`);
    if (pages.length >= maxPages || stats.liveBytes + size > maxLiveBytes) {
      return fallback(`capacity exhausted (${stats.liveBytes}/${maxLiveBytes} bytes live)`);
    }

    const state = saveGLState();
    let buffer = null;
    try {
      buffer = gl.createBuffer();
      if (!buffer) return fallback('pixel-pack buffer allocation failed');
      gl.bindBuffer(gl.PIXEL_PACK_BUFFER, buffer);
      gl.bufferData(gl.PIXEL_PACK_BUFFER, size, gl.STREAM_READ);
    } catch (error) {
      if (buffer) gl.deleteBuffer(buffer);
      return fallback(`pixel-pack buffer preparation failed: ${error && error.message || error}`);
    } finally {
      restoreGLState(state);
    }
    const page = { buffer, capacity: size, used: 0, descriptors: [], fence: null };
    pages.push(page);
    stats.liveBytes += size;
    stats.peakBytes = Math.max(stats.peakBytes, stats.liveBytes);
    return page;
  };

  const enqueue = (texture, width, height, id) => {
    if (stats.disposed) return fallback('manager is disposed');
    if (!available) return fallback('WebGL2 PBO readback APIs are unavailable');
    if (gl.isContextLost && gl.isContextLost()) fail('context lost before readback');
    if (!texture || !Number.isSafeInteger(width) || !Number.isSafeInteger(height) || width <= 0 || height <= 0 ||
        !Number.isSafeInteger(id) || id <= 0) return fallback('invalid texture descriptor');
    const byteLength = width * height * 4;
    if (!Number.isSafeInteger(byteLength) || byteLength > maxImageBytes) {
      return fallback(`texture size ${byteLength} exceeds the ${maxImageBytes}-byte PBO page limit`);
    }
    if (!ensureFramebuffer()) return fallback('read framebuffer preparation failed');
    const page = allocatePage(byteLength);
    if (!page) return null;
    const offset = page.used;
    const state = saveGLState();
    let readIssued = false;
    try {
      gl.bindFramebuffer(gl.READ_FRAMEBUFFER, framebuffer);
      gl.framebufferTexture2D(gl.READ_FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, texture, 0);
      if (gl.checkFramebufferStatus(gl.READ_FRAMEBUFFER) !== gl.FRAMEBUFFER_COMPLETE) {
        return fallback('texture framebuffer is incomplete');
      }
      gl.readBuffer(gl.COLOR_ATTACHMENT0);
      gl.bindBuffer(gl.PIXEL_PACK_BUFFER, page.buffer);
      gl.pixelStorei(gl.PACK_ALIGNMENT, 1);
      gl.pixelStorei(gl.PACK_ROW_LENGTH, 0);
      gl.pixelStorei(gl.PACK_SKIP_PIXELS, 0);
      gl.pixelStorei(gl.PACK_SKIP_ROWS, 0);
      // The readPixels command is ordered before any subsequent texture delete.
      readIssued = true;
      gl.readPixels(0, 0, width, height, gl.RGBA, gl.UNSIGNED_BYTE, offset);
    } catch (error) {
      if (readIssued) fail(`readPixels failed for texture ${id}: ${error && error.message || error}`);
      return fallback(`readback resource preparation failed: ${error && error.message || error}`);
    } finally {
      restoreGLState(state);
    }
    const descriptor = { id, width, height, byteLength, offset, texture, pixels: null };
    page.used += byteLength;
    page.descriptors.push(descriptor);
    return descriptor;
  };

  const checkCancelled = options => {
    if (options && typeof options.cancelled === 'function' && options.cancelled()) fail('readback cancelled');
  };

  const drain = async (options = {}) => {
    if (stats.disposed) fail('manager is disposed');
    const deadline = Number.isFinite(options.deadline) ? options.deadline : Date.now() + 30000;
    const active = pages.filter(page => page.used > 0 && page.descriptors.length > 0);
    if (active.length === 0) return;
    checkCancelled(options);
    if (gl.isContextLost && gl.isContextLost()) fail('context lost before fence');
    for (const page of active) {
      page.fence = gl.fenceSync(gl.SYNC_GPU_COMMANDS_COMPLETE, 0);
      if (!page.fence) fail('could not create completion fence');
    }
    gl.flush();
    for (const page of active) {
      const fenceWaitStarted = now();
      for (;;) {
        checkCancelled(options);
        if (Date.now() >= deadline) fail('fence wait timed out');
        if (gl.isContextLost && gl.isContextLost()) fail('context lost while waiting for readback');
        const result = gl.clientWaitSync(page.fence, 0, 0);
        if (result === gl.WAIT_FAILED) fail('fence wait returned WAIT_FAILED');
        if (result === gl.ALREADY_SIGNALED || result === gl.CONDITION_SATISFIED) break;
        if (result !== gl.TIMEOUT_EXPIRED) fail(`fence wait returned unexpected status ${result}`);
        // Yield to the browser event loop instead of spinning on an unsignaled fence.
        await new Promise(resolve => setTimeout(resolve, 1));
      }
      stats.fenceWaitWallMs += now() - fenceWaitStarted;
      const pixels = new Uint8Array(page.used);
      const state = saveGLState();
      const copyStarted = now();
      try {
        gl.bindBuffer(gl.PIXEL_PACK_BUFFER, page.buffer);
        gl.getBufferSubData(gl.PIXEL_PACK_BUFFER, 0, pixels);
      } catch (error) {
        fail(`getBufferSubData failed: ${error && error.message || error}`);
      } finally {
        stats.copyWallMs += now() - copyStarted;
        restoreGLState(state);
      }
      for (const descriptor of page.descriptors) {
        descriptor.pixels = pixels.subarray(descriptor.offset, descriptor.offset + descriptor.byteLength);
        descriptor.texture = null;
      }
      stats.transferBytes += page.used;
      gl.deleteSync(page.fence);
      page.fence = null;
      page.used = 0;
      page.descriptors = [];
    }
    stats.drainCalls++;
  };

  const dispose = () => {
    if (stats.disposed) return;
    stats.disposed = true;
    for (const page of pages) {
      if (page.fence) gl.deleteSync(page.fence);
      gl.deleteBuffer(page.buffer);
      for (const descriptor of page.descriptors) descriptor.texture = null;
      page.fence = null;
      page.used = 0;
      page.descriptors = [];
    }
    pages.length = 0;
    stats.liveBytes = 0;
    if (framebuffer) gl.deleteFramebuffer(framebuffer);
    framebuffer = null;
  };

  const getStats = () => ({
    ...stats,
    available,
    livePages: pages.length,
    pendingDescriptors: pendingCount(),
    queuedBytes: pages.reduce((total, page) => total + page.used, 0)
  });

  window.__nicoTimelinePBO = { available, enqueue, drain, dispose, getStats };
})();
