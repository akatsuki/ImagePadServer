// Comment-level capture bridge for the pinned niconicomments bundle.
// Install after sprites.js so all browser image primitives use the existing
// premultiplied WebGL readback path.
(() => {
  const unsupported = reason => {
    window.__nicoTimelineInstallError = reason;
    return;
  };
  const invalid = reason => { throw new Error(`NCT_CAPTURE_INVALID:${reason}`); };
  const n = window.__niconi;
  if (!n || !Array.isArray(n.comments) || !n.timeline || !n.renderer) return unsupported('renderer symbols missing');
  if (typeof n.getCommentPos !== 'function' || typeof n.sortTimelineComment !== 'function' || typeof n.drawCanvas !== 'function') {
    return unsupported('pinned bundle methods missing');
  }
  if (n.renderer.rendererName !== 'WebGL2Renderer' || typeof n.renderer.flush !== 'function') {
    return unsupported('WebGL2 sprite renderer required');
  }
  if (!n.ctx || !n.ctx.config || !n.ctx.options || !n.ctx.nicoScripts) return unsupported('renderer context missing');

  const indexByComment = new Map();
  for (let i = 0; i < n.comments.length; i++) {
    const comment = n.comments[i];
    if (!comment || comment.index !== i) invalid(`comment index mismatch at ${i}`);
    indexByComment.set(comment, i);
  }

  // Resolve placement before Go decides which output intervals need capture.
  // The pinned bundle may leave part of the comment array unprocessed until
  // this public layout pass has reached the requested end index.
  n.getCommentPos(n.comments, n.comments.length);
  n.sortTimelineComment();

  const ranges = new Map();
  for (const key of Object.keys(n.timeline)) {
    const vpos = Number(key);
    if (!Number.isSafeInteger(vpos)) invalid(`non-integer timeline key ${key}`);
    const comments = n.timeline[key];
    if (!Array.isArray(comments)) invalid(`timeline value ${key} is not an array`);
    let lastOwner = -1;
    let lastIndex = -1;
    const seenAtVpos = new Set();
    for (const comment of comments) {
      const index = indexByComment.get(comment);
      if (index === undefined) return unsupported('timeline contains an unregistered or plugin-injected comment');
      if (seenAtVpos.has(comment)) invalid(`duplicate timeline membership at ${key}`);
      seenAtVpos.add(comment);
      const owner = comment.owner === true ? 1 : 0;
      if (owner < lastOwner || owner === lastOwner && index < lastIndex) invalid(`TIMELINE_COMMENT_SORT mismatch at ${key}`);
      lastOwner = owner;
      lastIndex = index;
      const range = ranges.get(comment) || { start: vpos, end: vpos + 1, count: 0 };
      range.start = Math.min(range.start, vpos);
      range.end = Math.max(range.end, vpos + 1);
      range.count++;
      ranges.set(comment, range);
    }
  }

  for (const [comment, range] of ranges) {
    if (range.count !== range.end - range.start) {
      return unsupported(`comment ${comment.index} has non-contiguous timeline membership`);
    }
  }

  const scripts = Object.values(n.ctx.nicoScripts).filter(value => Array.isArray(value) && value.length > 0);
  if (scripts.length > 0) return unsupported('NicoScript state changes output over time');
  const runtimePlugins = n.plugins;
  const configuredRendererPlugins = n.ctx.config.plugins;
  const configuredCommentPlugins = n.ctx.config.commentPlugins;
  if (!Array.isArray(runtimePlugins) || !Array.isArray(configuredRendererPlugins) || !Array.isArray(configuredCommentPlugins)) {
    return unsupported('plugin configuration shape is unknown');
  }
  const pinnedFlashPlugin = window.NiconiComments && window.NiconiComments.FlashComment;
  const hasOnlyPinnedFlashFactory = configuredCommentPlugins.length === 1 && pinnedFlashPlugin &&
    configuredCommentPlugins[0].class === pinnedFlashPlugin.class &&
    configuredCommentPlugins[0].condition === pinnedFlashPlugin.condition;
  if (runtimePlugins.length > 0 || configuredRendererPlugins.length > 0 ||
      (configuredCommentPlugins.length > 0 && !hasOnlyPinnedFlashFactory)) {
    return unsupported('custom or active plugin rendering is not in the comment timeline allowlist');
  }
  if (n.ctx.options.keepCA || n.ctx.options.commentLimit !== undefined || n.showCollision || n.showFPS || n.showCommentCount || n.enableLegacyPiP) {
    return unsupported('renderer options add dynamic or non-comment output');
  }

  const finite = Number.isFinite;
  const comments = [];
  for (let i = 0; i < n.comments.length; i++) {
    const c = n.comments[i];
    const range = ranges.get(c);
    const speedX = c.loc === 'naka'
      ? -(n.ctx.config.commentDrawRange + c.width * n.ctx.config.nakaCommentSpeedOffset) / (c.long + 100)
      : 0;
    if (!range) {
      if (!Number.isInteger(c.vpos)) invalid(`comment ${i} has a non-finite vpos`);
      if (!c.invisible) return unsupported(`comment ${i} has no resolved timeline interval`);
      continue;
    }
    let reason = '';
    if (c.constructor && c.constructor.name !== 'HTML5Comment') reason = `comment ${i} uses ${c.constructor.name}`;
    else if (!['naka', 'ue', 'shita'].includes(c.loc)) reason = `comment ${i} has unknown location`;
    else if (!Number.isInteger(c.vpos) || !Number.isInteger(c.long) || c.long <= 0 ||
             !finite(c.width) || c.width <= 0 || !finite(c.height) || c.height <= 0 ||
             !finite(c.posY) || c.posY < 0) reason = `comment ${i} has unresolved geometry`;
    else if (c.flash || c.button || c.fillColor || c.wakuColor || c.strokeColor || c.full || c.ender || c._live || c.ignoreScale || c.commandScale) {
      reason = `comment ${i} uses unsupported decoration or rendering mode`;
    } else if (c.opacity !== undefined && (!finite(c.opacity) || c.opacity < 0 || c.opacity > 1)) {
      reason = `comment ${i} has invalid opacity`;
    }
    comments.push({
      index: i,
      ownerOrder: c.owner === true ? 1 : 0,
      loc: c.loc,
      vpos: c.vpos,
      startVPos: range.start,
      endVPos: range.end,
      x: Number.isFinite(c.pos && c.pos.x) ? c.pos.x : 0,
      y: c.posY,
      width: c.width,
      height: c.height,
      speedX,
      unsupported: reason
    });
  }

  const r = n.renderer;
  const originalDrawCanvas = n.drawCanvas;
  let drawCanvasCalls = 0;
  let elementDrawCalls = 0;
  let capturedAssets = 0;
  let capturedBytes = 0;
  let captureMaxAssets = 0;
  let captureMaxBytes = 0;
  let captureMaxElementBytes = 0;
  let captureElementByteStart = 0;
  let pendingCaptureElement = null;
  const originalCreateTexture = r._createTexture.bind(r);
  r._createTexture = source => {
    if (source === (r.helper && r.helper.canvas)) throw new Error('NCT_CAPTURE_UNSUPPORTED:dynamic helper canvas texture');
    const width = Number(source && source.width);
    const height = Number(source && source.height);
    if (!Number.isInteger(width) || !Number.isInteger(height) || width <= 0 || height <= 0 ||
        width > 16384 || height > 16384 || width * height * 4 > 128 * 1024 * 1024) {
      throw new Error('NCT_CAPTURE_UNSUPPORTED:texture dimension or byte limit exceeded before readback');
    }
    const bytes = width * height * 4;
    if (capturedAssets + 1 > captureMaxAssets || capturedBytes + bytes > captureMaxBytes ||
        capturedBytes - captureElementByteStart + bytes > captureMaxElementBytes) {
      throw new Error('NCT_CAPTURE_UNSUPPORTED:capture asset budget exceeded before readback');
    }
    capturedAssets++;
    capturedBytes += bytes;
    return originalCreateTexture(source);
  };
  n.drawCanvas = function (...args) {
    drawCanvasCalls++;
    return originalDrawCanvas.apply(this, args);
  };

  window.__nicoTimelineDescribe = (frameStart, frameEnd) => {
    if (!Number.isSafeInteger(frameStart) || !Number.isSafeInteger(frameEnd) || frameEnd <= frameStart) invalid('invalid output vpos interval');
    const active = comments.filter(comment => comment.startVPos < frameEnd && comment.endVPos > frameStart);
    const unsupportedComments = active.filter(comment => comment.unsupported).length;
    const reason = active.find(comment => comment.unsupported);
    const config = n.ctx.config;
    const described = active.map(comment => {
      const clippedStart = Math.max(comment.startVPos, frameStart);
      const clippedEnd = Math.min(comment.endVPos, frameEnd);
      const probeVPos = clippedStart + Math.floor((clippedEnd - clippedStart - 1) / 2);
      let x;
      if (comment.loc === 'naka') {
        const speed = -comment.speedX;
        const vposLapsed = probeVPos - comment.vpos;
        x = config.commentDrawPadding + config.commentDrawRange - (vposLapsed + 100) * speed;
      } else {
        x = (config.canvasWidth - comment.width) / 2;
      }
      const y = comment.loc === 'shita'
        ? config.canvasHeight - comment.y - comment.height
        : comment.y;
      return { ...comment, probeVPos, probeX: x, probeY: y };
    });
    return {
      ok: unsupportedComments === 0,
      reason: reason ? reason.unsupported : '',
      unsupportedComments,
      comments: described,
      drawCanvasCalls,
      elementDrawCalls
    };
  };

  window.__nicoTimelineCaptureElement = async (index, probeVposes, maxAssets, maxBytes, deferPBO = false) => {
    if (!Number.isInteger(index) || index < 0 || index >= n.comments.length) invalid('comment index outside renderer array');
    if (!Array.isArray(probeVposes) || probeVposes.length !== 1 ||
        probeVposes.some(v => !Number.isSafeInteger(v))) invalid('invalid element probe set');
    if (!Number.isInteger(maxAssets) || maxAssets <= 0 || !Number.isSafeInteger(maxBytes) || maxBytes <= 0) invalid('invalid capture budget');
    captureMaxAssets = maxAssets;
    captureMaxBytes = maxBytes;
    captureMaxElementBytes = Math.min(maxBytes, 32 * 1024 * 1024);
    captureElementByteStart = capturedBytes;
    const comment = n.comments[index];
    const samples = [];
    let deferredPBO = false;
    for (const vpos of probeVposes) {
      elementDrawCalls++;
      comment.draw(vpos, false, undefined);
      const position = comment.pos;
      const config = n.ctx.config;
      const outsideStage = position && (
        position.x + comment.width <= 0 || position.x >= config.canvasWidth ||
        position.y + comment.height <= 0 || position.y >= config.canvasHeight
      );
      if (outsideStage) {
        if (typeof comment._draw !== 'function') return unsupported(`comment ${index} cannot materialize its off-stage text image`);
        // draw() culls fully off-stage comments before it creates their cached
        // CanvasRenderer image. Materialize the same image at its real position
        // so later visible frames have a stable texture and affine draw record.
        comment._draw(position.x, position.y, undefined);
      }
      r.flush();
      const batch = await window.__nicoSpritesTakeForTimeline();
      deferredPBO = deferredPBO || batch.deferredPBO === true;
      const commands = [];
      for (const frame of batch.frames || []) {
        if (commands.length + (frame.commands || []).length > 4096) throw new Error('NCT_CAPTURE_UNSUPPORTED:too many primitives in one comment capture');
        if (!Array.isArray(frame.commands)) invalid('sprite command frame malformed');
        for (const command of frame.commands) commands.push(command);
      }
      if ((batch.textures || []).length > 512) throw new Error('NCT_CAPTURE_UNSUPPORTED:too many assets in one comment capture');
      samples.push({ vpos, x: comment.pos && comment.pos.x, y: comment.pos && comment.pos.y, commands, textures: batch.textures || [] });
    }
    if (drawCanvasCalls !== 0) invalid('drawCanvas called during element extraction');
    const element = {
      index,
      samples,
      drawCanvasCalls,
      elementDrawCalls,
      spriteMetrics: window.__nicoSpritesStats(),
      deferredPBO
    };
    if (element.deferredPBO && !deferPBO) {
      await window.__nicoSpritesDrainTimelinePBO();
      await window.__nicoSpritesFinalizeTimelineElement(element);
      element.spriteMetrics = window.__nicoSpritesStats();
    }
    return element;
  };

  window.__nicoTimelineCaptureBatch = async (requests, maxAssets, maxBytes, maxBatchBytes) => {
    if (!Array.isArray(requests) || requests.length < 1 || requests.length > 32) invalid('invalid capture batch size');
    if (!Number.isSafeInteger(maxBatchBytes) || maxBatchBytes <= 0) invalid('invalid capture batch response limit');
    const items = [];
    let responseBytes = 2; // JSON array brackets.
    let requestIndex = 0;
    if (pendingCaptureElement !== null) {
      if (!requests[0] || requests[0].index !== pendingCaptureElement.index) invalid('capture batch retry does not match pending element');
      items.push(pendingCaptureElement);
      responseBytes += JSON.stringify(pendingCaptureElement).length;
      pendingCaptureElement = null;
      requestIndex = 1;
    }
    for (; requestIndex < requests.length; requestIndex++) {
      const pendingPixelBytes = window.__nicoSpritesTimelinePendingPixelBytes(items);
      window.__nicoSpritesRecordTimelinePendingPixelBytes(pendingPixelBytes);
      if (pendingPixelBytes + 32 * 1024 * 1024 > 64 * 1024 * 1024) break;
      const request = requests[requestIndex];
      if (!request || !Number.isInteger(request.index) || !Array.isArray(request.probeVposes)) invalid('malformed capture batch request');
      const captured = await window.__nicoTimelineCaptureElement(request.index, request.probeVposes, maxAssets, maxBytes, true);
      const itemBytes = captured.deferredPBO
        ? window.__nicoSpritesEstimateTimelineElementLength(captured)
        : JSON.stringify(captured).length;
      const separatorBytes = items.length === 0 ? 0 : 1;
      // The capture response contains only numeric command fields and base64
      // texture payloads, so JavaScript string length is its UTF-8 byte size.
      // Leave room for the surrounding result object in the CDP response.
      if (items.length > 0 && responseBytes + separatorBytes + itemBytes + 32 > maxBatchBytes) {
        pendingCaptureElement = captured;
        window.__nicoSpritesRecordTimelinePendingPixelBytes(
          window.__nicoSpritesTimelinePendingPixelBytes(items.concat([pendingCaptureElement]))
        );
        break;
      }
      items.push(captured);
      window.__nicoSpritesRecordTimelinePendingPixelBytes(window.__nicoSpritesTimelinePendingPixelBytes(items));
      responseBytes += separatorBytes + itemBytes;
    }
    if (window.__nicoTimelinePBO && window.__nicoTimelinePBO.available) {
      await window.__nicoSpritesDrainTimelinePBO();
      for (let i = 0; i < items.length; i++) {
        if (items[i].deferredPBO) items[i] = await window.__nicoSpritesFinalizeTimelineElement(items[i]);
      }
      if (pendingCaptureElement && pendingCaptureElement.deferredPBO) {
        pendingCaptureElement = await window.__nicoSpritesFinalizeTimelineElement(pendingCaptureElement);
      }
      const metrics = window.__nicoSpritesStats();
      for (const item of items) item.spriteMetrics = metrics;
      if (pendingCaptureElement) pendingCaptureElement.spriteMetrics = metrics;
    }
    return { consumed: items.length, items };
  };
  window.__nicoTimelineCaptureReady = true;
})();
