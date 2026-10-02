export function stillImages(post) {
  if (!post) return []

  const isPhotoAssetURL = (rawURL) => {
    try {
      const parsed = new URL(rawURL)
      const host = parsed.hostname.toLowerCase().replace(/^www\./, '')
      return ['http:', 'https:'].includes(parsed.protocol) &&
        !['t.co', 'pic.x.com', 'pic.twitter.com', 'x.com', 'twitter.com'].includes(host)
    } catch {
      return false
    }
  }
  const mediaDetails = Array.isArray(post.mediaDetails) ? post.mediaDetails : []
  const entityMedia = Array.isArray(post.entities?.media) ? post.entities.media : []
  const postPhotos = Array.isArray(post.photos) ? post.photos : []
  const toPhoto = (candidate) => {
    if (!candidate) return null
    const url = candidate.url || candidate.media_url_https || ''
    const width = candidate.width || candidate.original_info?.width || 0
    const height = candidate.height || candidate.original_info?.height || 0
    return isPhotoAssetURL(url) && width > 0 && height > 0 ? { url, width, height } : null
  }

  const stillPhotos = []
  const seenPhotoURLs = new Set()
  const photoCandidates = [
    ...postPhotos,
    ...mediaDetails.filter((media) => media.type === 'photo'),
    ...entityMedia.filter((media) => media.type === 'photo'),
  ]
  for (const candidate of photoCandidates) {
    const photo = toPhoto(candidate)
    if (!photo || seenPhotoURLs.has(photo.url)) continue
    stillPhotos.push(photo)
    seenPhotoURLs.add(photo.url)
    if (stillPhotos.length === 4) break
  }
  if (stillPhotos.length > 0) return stillPhotos

  const videoMedia = [...mediaDetails, ...entityMedia]
    .find((media) => media.type === 'video' || media.type === 'animated_gif')
  const video = post.video ?? {}
  const url = video.poster || videoMedia?.media_url_https || ''
  if (!url) return null

  const ratio = video.aspectRatio || videoMedia?.video_info?.aspect_ratio || []
  const width = videoMedia?.original_info?.width || ratio[0] || 0
  const height = videoMedia?.original_info?.height || ratio[1] || 0
  return [{ url, width, height }]
}

export function firstStillImage(post) {
  return stillImages(post)[0] ?? null
}
