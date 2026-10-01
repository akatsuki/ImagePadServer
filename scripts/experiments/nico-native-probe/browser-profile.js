// Test-only hooks. Inject this before the page constructs NiconiComments.
(() => {
  const counters = () => ({
    constructorCalls: 0, constructorMs: 0,
    strokeTextCalls: 0, strokeTextMs: 0, fillTextCalls: 0, fillTextMs: 0,
    measureTextCalls: 0, measureTextMs: 0, drawImageCalls: 0, drawImageMs: 0,
    readPixelsCalls: 0, readPixelsMs: 0, texImage2DCalls: 0, texImage2DMs: 0,
  });
  const total = counters();
  let current = counters();
  const add = (name, elapsed) => {
    current[name] += 1;
    total[name] += 1;
    const msName = name.replace(/Calls$/, 'Ms');
    current[msName] += elapsed;
    total[msName] += elapsed;
  };
  const timed = (proto, name, callName) => {
    if (!proto || typeof proto[name] !== 'function') return;
    const original = proto[name];
    proto[name] = function(...args) {
      const started = performance.now();
      try { return original.apply(this, args); }
      finally { add(callName + 'Calls', performance.now() - started); }
    };
  };
  const contextPrototypes = [
    window.CanvasRenderingContext2D && window.CanvasRenderingContext2D.prototype,
    window.OffscreenCanvasRenderingContext2D && window.OffscreenCanvasRenderingContext2D.prototype,
  ].filter((proto, index, all) => proto && all.indexOf(proto) === index);
  for (const proto of contextPrototypes) {
    timed(proto, 'strokeText', 'strokeText');
    timed(proto, 'fillText', 'fillText');
    timed(proto, 'measureText', 'measureText');
    timed(proto, 'drawImage', 'drawImage');
  }
  timed(window.WebGL2RenderingContext && WebGL2RenderingContext.prototype, 'readPixels', 'readPixels');
  timed(window.WebGL2RenderingContext && WebGL2RenderingContext.prototype, 'texImage2D', 'texImage2D');

  const Original = window.NiconiComments;
  if (typeof Original !== 'function') throw Error('NiconiComments constructor is unavailable');
  window.NiconiComments = new Proxy(Original, {
    construct(target, args, newTarget) {
      const started = performance.now();
      try { return Reflect.construct(target, args, newTarget); }
      finally { add('constructorCalls', performance.now() - started); }
    },
  });

  const snapshot = () => ({ ...current });
  const reset = () => { current = counters(); };
  window.__nicoNativeProfileTake = () => { const out = snapshot(); reset(); return out; };
  window.__nicoNativeProfileSnapshot = () => ({ current: snapshot(), total: { ...total } });
  window.__nicoNativeProfileReady = true;
})();
