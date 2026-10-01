'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const sourcePath = path.join(__dirname, 'sprites.js');
const spriteSource = fs.readFileSync(sourcePath, 'utf8');

function createHarness({ withNativeToBase64 = false, nativeThrows = false } = {}) {
  const calls = { native: 0, btoa: 0, fromCharCode: 0 };
  const gl = {
    TEXTURE_2D: 1, FRAMEBUFFER: 2, FRAMEBUFFER_BINDING: 3, COLOR_ATTACHMENT0: 4,
    FRAMEBUFFER_COMPLETE: 5, TRIANGLE_STRIP: 6, RGBA: 7, UNSIGNED_BYTE: 8,
    bindTexture() {}, useProgram() {}, uniform4f() {}, uniform1f() {},
    uniformMatrix4fv() {}, drawArrays() {}, deleteTexture() {},
    getParameter() { return null; }, createFramebuffer() { return {}; },
    bindFramebuffer() {}, framebufferTexture2D() {},
    checkFramebufferStatus() { return 5; }, readPixels() {}, deleteFramebuffer() {}
  };
  const renderer = {
    rendererName: 'WebGL2Renderer', gl, _createTexture() { return {}; }, flush() {},
    spriteProg: {}, rectProg: {}, spriteLocRect: {}, spriteLocProj: {}, spriteLocAlpha: {},
    rectLocRect: {}, rectLocProj: {}, rectLocColor: {}
  };
  const window = { __niconi: { renderer } };
  let tick = 0;
  const context = vm.createContext({
    window,
    Buffer,
    __spriteFastPathCalls: calls,
    btoa(text) {
      calls.btoa++;
      return Buffer.from(text, 'latin1').toString('base64');
    },
    performance: { now: () => ++tick }
  });
  vm.runInContext(`
    const originalFromCharCode = String.fromCharCode;
    String.fromCharCode = function (...values) {
      globalThis.__spriteFastPathCalls.fromCharCode++;
      return originalFromCharCode.apply(this, values);
    };
  `, context);
  vm.runInContext('delete Uint8Array.prototype.toBase64', context);
  if (withNativeToBase64) {
    vm.runInContext(`Uint8Array.prototype.toBase64 = function () {
      globalThis.__spriteFastPathCalls.native++;
      if (${nativeThrows}) throw new Error('native toBase64 sentinel');
      return Buffer.from(this).toString('base64');
    }`, context);
  }
  vm.runInContext(spriteSource, context, { filename: sourcePath });
  return { context, calls };
}

function patternBase64(length, multiplier = 131, offset = 17) {
  const bytes = Buffer.allocUnsafe(length);
  for (let i = 0; i < length; i++) bytes[i] = (i * multiplier + offset) & 0xff;
  return bytes.toString('base64');
}

function assertBase64(actual, expected, name) {
  if (actual === expected) return;
  let firstDifference = 0;
  while (firstDifference < actual.length && firstDifference < expected.length && actual[firstDifference] === expected[firstDifference]) {
    firstDifference++;
  }
  throw new Error(`${name} Base64 mismatch: actual=${actual.length} chars, expected=${expected.length} chars, first difference at ${firstDifference}`);
}

function fixtureCases() {
  return [
    { name: 'empty', expression: 'new Uint8Array([])', expected: '' },
    { name: 'one byte with padding', expression: 'new Uint8Array([0])', expected: 'AA==' },
    { name: 'two bytes with padding', expression: 'new Uint8Array([0, 255])', expected: 'AP8=' },
    { name: 'three bytes without padding', expression: 'new Uint8Array([0, 255, 127])', expected: 'AP9/' },
    {
      name: 'all byte values',
      expression: 'Uint8Array.from({ length: 256 }, (_, i) => i)',
      expected: Buffer.from(Array.from({ length: 256 }, (_, i) => i)).toString('base64')
    },
    {
      name: 'non-zero-offset subarray',
      expression: 'new Uint8Array([238, 0, 255, 127, 128, 221]).subarray(1, 5)',
      expected: 'AP9/gA=='
    },
    ...[16383, 16384, 16385].map(length => ({
      name: `chunk boundary ${length}`,
      expression: `Uint8Array.from({ length: ${length} }, (_, i) => (i * 131 + 17) & 255)`,
      expected: patternBase64(length)
    })),
    {
      name: '1080p RGBA byte payload',
      expression: 'Uint8Array.from({ length: 1920 * 1080 * 4 }, (_, i) => (i * 31 + 9) & 255)',
      expected: patternBase64(1920 * 1080 * 4, 31, 9)
    }
  ];

}

function runFixtures(harness, mode) {
  const cases = fixtureCases();
  for (const fixture of cases) {
    const hook = harness.context.window.__nicoSpritesEncodeBytesForTest;
    assert.equal(typeof hook, 'function', 'sprite encoder test hook is missing');
    const modeArg = mode === undefined ? '' : `, ${JSON.stringify(mode)}`;
    const actual = vm.runInContext(`window.__nicoSpritesEncodeBytesForTest(${fixture.expression}${modeArg})`, harness.context);
    assertBase64(actual, fixture.expected, fixture.name);
  }
  return cases.length;
}

function runNativeFixture(harness) {
  const actual = vm.runInContext('window.__nicoSpritesEncodeBytesForTest(new Uint8Array([0, 255, 127]))', harness.context);
  assert.equal(actual, 'AP9/', 'native path changed the Base64 output');
  if (harness.calls.native !== 1 || harness.calls.btoa !== 0 || harness.calls.fromCharCode !== 0) {
    throw new Error(`native toBase64 call mismatch: native=${harness.calls.native}, btoa=${harness.calls.btoa}, fromCharCode=${harness.calls.fromCharCode}`);
  }
}

function runForcedLegacyFixture(harness) {
  const actual = vm.runInContext("window.__nicoSpritesEncodeBytesForTest(new Uint8Array([0, 255]), 'legacy')", harness.context);
  assert.equal(actual, 'AP8=', 'forced legacy path changed padded Base64 output');
  assert.equal(harness.calls.native, 0, 'forced legacy mode called native toBase64');
  assert.equal(harness.calls.btoa, 1, 'forced legacy mode did not call btoa exactly once');
  assert.equal(harness.calls.fromCharCode, 1, 'forced legacy mode did not stringify its byte chunk exactly once');
}

function runMissingNativeFallbackFixture(harness) {
  const actual = vm.runInContext('window.__nicoSpritesEncodeBytesForTest(new Uint8Array([0, 255]))', harness.context);
  assert.equal(actual, 'AP8=', 'API-missing fallback changed padded Base64 output');
  assert.equal(harness.calls.native, 0, 'missing native API unexpectedly recorded a native call');
  assert.equal(harness.calls.btoa, 1, 'missing native API did not use legacy btoa exactly once');
  assert.equal(harness.calls.fromCharCode, 1, 'missing native API did not stringify its byte chunk exactly once');
}

function runNativeExceptionFixture(harness) {
  assert.throws(
    () => vm.runInContext('window.__nicoSpritesEncodeBytesForTest(new Uint8Array([1]))', harness.context),
    /native toBase64 sentinel/
  );
  assert.equal(harness.calls.native, 1, 'throwing native path was not called exactly once');
  assert.equal(harness.calls.btoa, 0, 'native exception was silently converted through btoa');
  assert.equal(harness.calls.fromCharCode, 0, 'native exception was silently converted through the legacy string path');
}

function trackedPack(harness, pixels, width, height) {
  const original = Uint8Array.from(pixels);
  const reads = { indexed: 0 };
  const tracked = new Proxy(original, {
    get(target, property) {
      if (typeof property === 'string' && /^(0|[1-9][0-9]*)$/.test(property)) reads.indexed++;
      if (property === 'subarray') return target.subarray.bind(target);
      return Reflect.get(target, property, target);
    }
  });
  harness.context.__paletteScanPixels = tracked;
  try {
    const packed = vm.runInContext(
      'window.__nicoSpritesPackTextureForTest(globalThis.__paletteScanPixels, ' + width + ', ' + height + ')',
      harness.context
    );
    return { packed, indexedReads: reads.indexed, original };
  } finally {
    delete harness.context.__paletteScanPixels;
  }
}

function grayPaletteOverflowPixels(count, nonGrayAt = -1) {
  const pixels = new Uint8Array(count * 4);
  for (let p = 0; p < count; p++) {
    const gray = p < 256 ? p : p === 256 ? 0 : 128;
    const alpha = p === 256 ? 254 : 255;
    if (p === nonGrayAt) pixels.set([1, 2, 3, 255], p * 4);
    else pixels.set([gray, gray, gray, alpha], p * 4);
  }
  return pixels;
}

function completePalettePixels(count, nonGrayAt) {
  const pixels = new Uint8Array(count * 4);
  let shade = 0;
  for (let p = 0; p < count; p++) {
    if (p === nonGrayAt) pixels.set([1, 2, 3, 255], p * 4);
    else {
      const gray = shade++ % 255;
      pixels.set([gray, gray, gray, 255], p * 4);
    }
  }
  return pixels;
}

function expectedPaletteEncoding(pixels) {
  const indexes = new Uint8Array(pixels.length / 4);
  const palette = [];
  const seen = new Map();
  for (let p = 0; p < indexes.length; p++) {
    const o = p * 4;
    const rgba = [pixels[o], pixels[o + 1], pixels[o + 2], pixels[o + 3]];
    const key = rgba.join(':');
    let index = seen.get(key);
    if (index === undefined) {
      index = palette.length / 4;
      seen.set(key, index);
      palette.push(...rgba);
    }
    indexes[p] = index;
  }
  return {
    data: Buffer.from(indexes).toString('base64'),
    palette: Buffer.from(palette).toString('base64')
  };
}

function assertRawTexture(packed, pixels, name) {
  assert.equal(packed.encoding, '', name + ' must use raw RGBA');
  assert.equal(packed.palette, '', name + ' must not emit a palette');
  assertBase64(packed.data, Buffer.from(pixels).toString('base64'), name + ' raw RGBA');
}

function runPaletteScanFixtures(harness) {
  const count = 1024;
  let fixtures = 0;

  for (const firstNonGrayAt of [0, 512, count - 1]) {
    const pixels = completePalettePixels(count, firstNonGrayAt);
    const result = trackedPack(harness, pixels, count, 1);
    const expected = expectedPaletteEncoding(pixels);
    assert.equal(result.packed.encoding, 'palette8', '256-entry palette at position ' + firstNonGrayAt);
    assertBase64(result.packed.data, expected.data, '256-entry palette indexes');
    assertBase64(result.packed.palette, expected.palette, '256-entry palette bytes');
    assert.equal(result.indexedReads, count * 4, 'a complete 256-entry palette must inspect every pixel');
    fixtures++;
  }

  const alphaOnly = new Uint8Array(count * 4);
  for (let p = 0; p < count; p++) alphaOnly.set([42, 42, 42, p % 256], p * 4);
  const alphaResult = trackedPack(harness, alphaOnly, count, 1);
  const expectedAlphaPalette = expectedPaletteEncoding(alphaOnly);
  assert.equal(alphaResult.packed.encoding, 'palette8', '256 alpha values for one gray RGB color remain palette eligible');
  assertBase64(alphaResult.packed.data, expectedAlphaPalette.data, 'same-RGB alpha palette indexes');
  assertBase64(alphaResult.packed.palette, expectedAlphaPalette.palette, 'same-RGB alpha palette bytes');
  assert.equal(alphaResult.indexedReads, count * 4, 'same-RGB alpha palette must inspect every pixel');
  fixtures++;

  const firstNonGray = new Uint8Array(count * 4);
  for (let p = 0; p < count; p++) {
    const color = p < 257 ? p : p % 257;
    firstNonGray.set([color & 255, (color >>> 8) & 255, (color * 73 + 17) & 255, 255], p * 4);
  }
  const uniqueOverflow = trackedPack(harness, firstNonGray, count, 1);
  assertRawTexture(uniqueOverflow.packed, firstNonGray, '257th non-gray palette entry');
  // T2.4 rejected the early-return candidate after repeated pack-only regressions on real comment assets.
  assert.equal(uniqueOverflow.indexedReads, count * 4, 'full-scan baseline should inspect every pixel after palette overflow');
  fixtures++;

  for (const firstNonGrayAt of [0, 512, count - 1]) {
    const pixels = grayPaletteOverflowPixels(count, firstNonGrayAt);
    const result = trackedPack(harness, pixels, count, 1);
    assertRawTexture(result.packed, pixels, 'gray palette overflow with non-gray at ' + firstNonGrayAt);
    assert.equal(
      result.indexedReads,
      count * 4,
      'full-scan baseline should inspect every pixel after palette overflow and non-gray pixel at ' + firstNonGrayAt
    );
    fixtures++;
  }

  const grayOnly = grayPaletteOverflowPixels(count);
  const grayResult = trackedPack(harness, grayOnly, count, 1);
  const expectedGrayAlpha = new Uint8Array(count * 2);
  for (let p = 0; p < count; p++) {
    expectedGrayAlpha[p * 2] = grayOnly[p * 4];
    expectedGrayAlpha[p * 2 + 1] = grayOnly[p * 4 + 3];
  }
  assert.equal(grayResult.packed.encoding, 'grayalpha8', '257 gray-alpha colors must retain grayalpha encoding');
  assert.equal(grayResult.packed.palette, '', 'grayalpha output must not carry a palette');
  assertBase64(grayResult.packed.data, Buffer.from(expectedGrayAlpha).toString('base64'), 'grayalpha bytes and alpha');
  assert.equal(grayResult.indexedReads, count * 6, 'grayalpha must scan every pixel and preserve its second gray/alpha pass');
  fixtures++;
  return fixtures;
}

function main() {
  const legacy = createHarness(false);
  const count = runFixtures(legacy);
  assert.ok(legacy.calls.btoa > 0, 'API-missing fixtures did not exercise the legacy btoa path');

  const native = createHarness({ withNativeToBase64: true });
  runNativeFixture(native);
  const nativeBoundaries = createHarness({ withNativeToBase64: true });
  const nativeCount = runFixtures(nativeBoundaries, 'native');
  assert.equal(nativeBoundaries.calls.native, nativeCount, 'native boundary fixtures must call toBase64 exactly once each');
  assert.equal(nativeBoundaries.calls.btoa, 0, 'native boundary fixtures must not call btoa');
  assert.equal(nativeBoundaries.calls.fromCharCode, 0, 'native boundary fixtures must not stringify bytes with fromCharCode');

  runForcedLegacyFixture(createHarness({ withNativeToBase64: true }));
  runMissingNativeFallbackFixture(createHarness());
  runNativeExceptionFixture(createHarness({ withNativeToBase64: true, nativeThrows: true }));
  const paletteCount = runPaletteScanFixtures(createHarness(false));

  process.stdout.write('PASS palette scan fixtures=' + paletteCount + ', rgba/palette/grayalpha exactness\n');
  process.stdout.write(`PASS Base64 legacy fixtures=${count}, native boundaries=${nativeCount}, fallback/override/exception\n`);
}

main();
