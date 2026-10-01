const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

const scriptPath = process.argv[2];
const source = fs.readFileSync(scriptPath, 'utf8');

function createGL(overrides = {}) {
  const gl = {
    PIXEL_PACK_BUFFER: 1,
    PIXEL_PACK_BUFFER_BINDING: 2,
    READ_FRAMEBUFFER: 3,
    READ_FRAMEBUFFER_BINDING: 4,
    READ_BUFFER: 5,
    FRAMEBUFFER: 6,
    COLOR_ATTACHMENT0: 7,
    TEXTURE_2D: 8,
    FRAMEBUFFER_COMPLETE: 9,
    PACK_ALIGNMENT: 10,
    PACK_ROW_LENGTH: 11,
    PACK_SKIP_PIXELS: 12,
    PACK_SKIP_ROWS: 13,
    STREAM_READ: 14,
    RGBA: 15,
    UNSIGNED_BYTE: 16,
    SYNC_GPU_COMMANDS_COMPLETE: 17,
    ALREADY_SIGNALED: 18,
    CONDITION_SATISFIED: 19,
    TIMEOUT_EXPIRED: 20,
    WAIT_FAILED: 21,
    state: new Map(),
    buffers: [],
    fences: [],
    deletedBuffers: 0,
    deletedFences: 0,
    deleteFramebuffers: 0,
    readPixelsCalls: 0,
    getBufferSubDataCalls: 0,
    flushCalls: 0,
    waitResults: [],
    nextID: 1,
    isContextLost: () => false,
    getParameter(key) { return this.state.get(key) ?? null; },
    createFramebuffer() { return { kind: 'framebuffer', id: this.nextID++ }; },
    deleteFramebuffer() { this.deleteFramebuffers++; },
    bindFramebuffer(target, value) { this.state.set(target === this.READ_FRAMEBUFFER ? this.READ_FRAMEBUFFER_BINDING : this.READ_FRAMEBUFFER_BINDING, value); },
    framebufferTexture2D() {},
    checkFramebufferStatus() { return this.FRAMEBUFFER_COMPLETE; },
    readBuffer(value) { this.state.set(this.READ_BUFFER, value); },
    pixelStorei(key, value) { this.state.set(key, value); },
    createBuffer() { const value = { kind: 'buffer', id: this.nextID++, storage: null }; this.buffers.push(value); return value; },
    deleteBuffer() { this.deletedBuffers++; },
    bindBuffer(target, value) { this.state.set(this.PIXEL_PACK_BUFFER_BINDING, value); },
    bufferData(target, size) { const value = this.state.get(this.PIXEL_PACK_BUFFER_BINDING); value.storage = new Uint8Array(size); },
    readPixels(_x, _y, _w, _h, _format, _type, offset) {
      this.readPixelsCalls++;
      const buffer = this.state.get(this.PIXEL_PACK_BUFFER_BINDING);
      const pixels = globalThis.pendingPixels.shift();
      buffer.storage.set(pixels, offset);
    },
    fenceSync() { const fence = { kind: 'fence', id: this.nextID++ }; this.fences.push(fence); return fence; },
    deleteSync() { this.deletedFences++; },
    clientWaitSync() { return this.waitResults.length ? this.waitResults.shift() : this.ALREADY_SIGNALED; },
    flush() { this.flushCalls++; },
    getBufferSubData(_target, offset, destination) {
      this.getBufferSubDataCalls++;
      const buffer = this.state.get(this.PIXEL_PACK_BUFFER_BINDING);
      destination.set(buffer.storage.subarray(offset, offset + destination.byteLength));
    }
  };
  gl.state.set(gl.PIXEL_PACK_BUFFER_BINDING, { kind: 'foreign-buffer' });
  gl.state.set(gl.READ_FRAMEBUFFER_BINDING, { kind: 'foreign-framebuffer' });
  gl.state.set(gl.READ_BUFFER, gl.COLOR_ATTACHMENT0);
  gl.state.set(gl.PACK_ALIGNMENT, 8);
  gl.state.set(gl.PACK_ROW_LENGTH, 9);
  gl.state.set(gl.PACK_SKIP_PIXELS, 2);
  gl.state.set(gl.PACK_SKIP_ROWS, 3);
  Object.assign(gl, overrides);
  return gl;
}

function install(gl, pageSizes = [16, 32], maxLiveBytes = 32) {
  globalThis.pendingPixels = [];
  const window = {
    __niconi: { renderer: { rendererName: 'WebGL2Renderer', gl } },
    __nicoTimelinePBOOptions: { pageSizes, maxLiveBytes }
  };
  vm.runInNewContext(source, {
    window,
    setTimeout,
    Promise,
    Uint8Array,
    ArrayBuffer,
    Error,
    Math,
    Number,
    Date,
    performance: { now: () => Date.now() }
  }, { filename: scriptPath });
  return window.__nicoTimelinePBO;
}

async function run() {
  {
    const gl = createGL();
    const manager = install(gl);
    assert.ok(manager && manager.available, 'PBO manager should initialize');
    const first = Uint8Array.from([1, 2, 3, 4, 5, 6, 7, 8]);
    const second = Uint8Array.from([21, 22, 23, 24]);
    globalThis.pendingPixels.push(first, second);
    const a = manager.enqueue({ id: 'a' }, 2, 1, 1);
    const b = manager.enqueue({ id: 'b' }, 1, 1, 2);
    assert.ok(a.texture && b.texture, 'texture references should remain alive until the page drains');
    await manager.drain();
    assert.deepEqual(Array.from(a.pixels), Array.from(first));
    assert.deepEqual(Array.from(b.pixels), Array.from(second));
    assert.equal(gl.readPixelsCalls, 2);
    assert.equal(gl.getBufferSubDataCalls, 1, 'one copy should drain the shared page');
    assert.equal(gl.fences.length, 1, 'one fence should seal the shared page');
    assert.equal(a.texture, null, 'texture references should be released after drain');
    assert.equal(gl.state.get(gl.PIXEL_PACK_BUFFER_BINDING).kind, 'foreign-buffer');
    assert.equal(gl.state.get(gl.READ_FRAMEBUFFER_BINDING).kind, 'foreign-framebuffer');
    assert.equal(gl.state.get(gl.READ_BUFFER), gl.COLOR_ATTACHMENT0);
    assert.equal(gl.state.get(gl.PACK_ALIGNMENT), 8);
    assert.equal(gl.state.get(gl.PACK_ROW_LENGTH), 9);
    assert.equal(gl.state.get(gl.PACK_SKIP_PIXELS), 2);
    assert.equal(gl.state.get(gl.PACK_SKIP_ROWS), 3);
    assert.equal(manager.getStats().pendingDescriptors, 0);
    manager.dispose();
    manager.dispose();
    assert.equal(manager.getStats().livePages, 0);
    assert.equal(manager.getStats().liveBytes, 0);
    assert.equal(gl.deletedBuffers, 1);
    assert.equal(gl.deletedFences, 1);
    assert.equal(gl.deleteFramebuffers, 1);
  }

  {
    const gl = createGL({ waitResults: [20, 18] });
    const manager = install(gl);
    globalThis.pendingPixels.push(Uint8Array.from([9, 8, 7, 6]));
    const descriptor = manager.enqueue({}, 1, 1, 1);
    await manager.drain();
    assert.deepEqual(Array.from(descriptor.pixels), [9, 8, 7, 6]);
    assert.equal(gl.waitResults.length, 0, 'timeout should yield and poll again');
  }

  {
    const gl = createGL();
    const manager = install(gl, [16], 32);
    const firstPixels = Uint8Array.from(new Array(12).fill(3));
    const secondPixels = Uint8Array.from(new Array(8).fill(7));
    globalThis.pendingPixels.push(firstPixels, secondPixels);
    const first = manager.enqueue({}, 3, 1, 1);
    const second = manager.enqueue({}, 2, 1, 2);
    assert.equal(manager.getStats().livePages, 2, 'a second page should be allocated when the first has insufficient space');
    await manager.drain();
    assert.deepEqual(Array.from(first.pixels), Array.from(firstPixels));
    assert.deepEqual(Array.from(second.pixels), Array.from(secondPixels));
    assert.equal(gl.fences.length, 2);
    assert.equal(gl.getBufferSubDataCalls, 2);
    manager.dispose();
  }

  {
    const gl = createGL();
    const manager = install(gl, [16, 32], 16);
    globalThis.pendingPixels.push(Uint8Array.from([1, 2, 3, 4]));
    assert.ok(manager.enqueue({}, 1, 1, 1));
    globalThis.pendingPixels.push(Uint8Array.from(new Array(16).fill(5)));
    assert.equal(manager.enqueue({}, 4, 1, 2), null, 'capacity exhaustion should select sync fallback');
    assert.match(manager.getStats().fallbackReason, /capacity/i);
    await manager.drain();
    manager.dispose();
  }

  {
    const gl = createGL({ waitResults: [21] });
    const manager = install(gl);
    globalThis.pendingPixels.push(Uint8Array.from([1, 2, 3, 4]));
    manager.enqueue({}, 1, 1, 1);
    await assert.rejects(manager.drain(), /WAIT_FAILED/i);
    manager.dispose();
    assert.equal(manager.getStats().livePages, 0);
    assert.equal(manager.getStats().liveBytes, 0);
  }

  {
    const gl = createGL({ isContextLost: () => true });
    const manager = install(gl);
    assert.throws(() => manager.enqueue({}, 1, 1, 1), /context lost/i);
    manager.dispose();
    assert.equal(manager.getStats().livePages, 0);
  }

  {
    let lost = false;
    const gl = createGL({
      isContextLost: () => lost,
      clientWaitSync() { lost = true; return this.TIMEOUT_EXPIRED; }
    });
    const manager = install(gl);
    globalThis.pendingPixels.push(Uint8Array.from([1, 2, 3, 4]));
    manager.enqueue({}, 1, 1, 1);
    await assert.rejects(manager.drain(), /context lost while waiting/i);
    manager.dispose();
    assert.equal(manager.getStats().livePages, 0);
    assert.equal(gl.deletedFences, 1);
  }

  {
    const gl = createGL({ getBufferSubData() { throw new Error('injected transfer error'); } });
    const manager = install(gl);
    globalThis.pendingPixels.push(Uint8Array.from([1, 2, 3, 4]));
    manager.enqueue({}, 1, 1, 1);
    await assert.rejects(manager.drain(), /getBufferSubData failed/i);
    manager.dispose();
    assert.equal(manager.getStats().livePages, 0);
    assert.equal(manager.getStats().liveBytes, 0);
    assert.equal(gl.deletedFences, 1);
  }

  {
    const gl = createGL();
    const manager = install(gl);
    globalThis.pendingPixels.push(Uint8Array.from([1, 2, 3, 4]));
    manager.enqueue({}, 1, 1, 1);
    await assert.rejects(manager.drain({ cancelled: () => true }), /cancel/i);
    manager.dispose();
    assert.equal(manager.getStats().livePages, 0);
    assert.equal(manager.getStats().liveBytes, 0);
  }
}

run().catch(error => { console.error(error); process.exitCode = 1; });
