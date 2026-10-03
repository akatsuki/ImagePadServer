const maxVideoBytes = 4 * 1024 ** 3 - 1

function isHTTPURL(rawURL) {
  try {
    const parsed = new URL(rawURL)
    return (parsed.protocol === 'https:' || parsed.protocol === 'http:') && Boolean(parsed.hostname)
  } catch {
    return false
  }
}

export function bestMP4Variant(videoInfo) {
  const variants = Array.isArray(videoInfo?.variants) ? videoInfo.variants : []
  const valid = variants.filter((variant) => {
    const contentType = String(variant?.content_type || '').toLowerCase()
    const url = String(variant?.url || '')
    const mp4 = contentType.includes('mp4') || /\.mp4(?:[?#]|$)/i.test(url)
    const declaredSize = Number(variant?.size ?? variant?.content_length ?? 0)
    return mp4 && isHTTPURL(url) && !(declaredSize > maxVideoBytes)
  })
  valid.sort((a, b) => Number(b.bitrate || 0) - Number(a.bitrate || 0))
  return valid[0] || null
}

function dimension(value) {
  const parsed = Number(value)
  return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : 0
}

export function videoMedia(post) {
  if (!post) return []
  const details = Array.isArray(post.mediaDetails) ? post.mediaDetails : []
  const entities = Array.isArray(post.entities?.media) ? post.entities.media : []
  const candidates = [...details, ...entities]
  if (post.video && !candidates.includes(post.video)) candidates.push(post.video)

  const seen = new Set()
  const result = []
  for (const candidate of candidates) {
    const kind = candidate?.type === 'animated_gif' ? 'animated_gif' :
      candidate?.type === 'video' || candidate === post.video ? 'video' : ''
    if (!kind) continue
    const info = candidate.video_info || candidate.videoInfo || post.video || {}
    const variant = bestMP4Variant(info)
    if (!variant) continue
    const identity = candidate.id_str || candidate.id || candidate.media_url_https || variant.url
    if (seen.has(identity)) continue
    seen.add(identity)
    const duration = Number(info.duration_millis ?? candidate.duration_millis ?? 0)
    result.push({
      kind,
      url: variant.url,
      width: 0,
      height: 0,
      duration: Number.isFinite(duration) && duration > 0 ? duration / 1000 : 0,
      hasAudio: Boolean(info.has_audio ?? candidate.has_audio),
    })
    if (result.length === 4) break
  }
  return result
}

export function postMedia(post) {
  if (!post) return []
  const details = Array.isArray(post.mediaDetails) ? post.mediaDetails : []
  const entities = Array.isArray(post.entities?.media) ? post.entities.media : []
  const candidates = [...details, ...entities]
  if (post.video && !candidates.includes(post.video)) candidates.push(post.video)

  const result = []
  const seen = new Set()
  for (const candidate of candidates) {
    const type = candidate?.type
    let asset
    if (type === 'photo') {
      const url = candidate.media_url_https || candidate.media_url || candidate.url || ''
      if (!isHTTPURL(url)) continue
      const original = candidate.original_info || {}
      asset = {
        kind: 'image',
        url,
        width: dimension(original.width || candidate.width),
        height: dimension(original.height || candidate.height),
      }
    } else if (type === 'video' || type === 'animated_gif' || candidate === post.video) {
      const info = candidate.video_info || candidate.videoInfo || post.video || {}
      const variant = bestMP4Variant(info)
      if (!variant) continue
      const duration = Number(info.duration_millis ?? candidate.duration_millis ?? 0)
      asset = {
        kind: type === 'animated_gif' ? 'animated_gif' : 'video',
        url: variant.url,
        width: 0,
        height: 0,
        duration: Number.isFinite(duration) && duration > 0 ? duration / 1000 : 0,
        hasAudio: Boolean(info.has_audio ?? candidate.has_audio),
      }
    } else {
      continue
    }
    const identity = candidate.id_str || candidate.id || candidate.media_url_https || asset.url
    if (seen.has(identity)) continue
    seen.add(identity)
    result.push(asset)
    if (result.length === 4) break
  }
  return result
}
