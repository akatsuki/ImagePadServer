import test from 'node:test'
import assert from 'node:assert/strict'
import { videoMedia, postMedia, bestMP4Variant } from './video_media.mjs'

test('videoMedia preserves original media order and caps assets at four', () => {
  const post = {
    mediaDetails: Array.from({ length: 5 }, (_, index) => ({
      id_str: `video-${index}`,
      type: 'video',
      video_info: {
        aspect_ratio: [16, 9],
        duration_millis: 1200,
        variants: [{ content_type: 'video/mp4', bitrate: 1000, url: `https://video.test/${index}.mp4` }],
      },
      original_info: { width: 1280, height: 720 },
    })),
  }

  const media = videoMedia(post)
  assert.deepEqual(media.map((entry) => entry.url), [
    'https://video.test/0.mp4', 'https://video.test/1.mp4',
    'https://video.test/2.mp4', 'https://video.test/3.mp4',
  ])
  assert.equal(media[0].kind, 'video')
  assert.equal(media[0].duration, 1.2)
  assert.equal(media[0].width, 0)
  assert.equal(media[0].height, 0)
})

test('bestMP4Variant ignores non-MP4 and over-limit variants, then picks highest bitrate', () => {
  const variant = bestMP4Variant({ variants: [
    { content_type: 'application/x-mpegURL', bitrate: 9000, url: 'https://video.test/master.m3u8' },
    { content_type: 'video/mp4', bitrate: 5000, size: 5 * 1024 ** 3, url: 'https://video.test/large.mp4' },
    { content_type: 'video/mp4', bitrate: 700, url: 'https://video.test/low.mp4' },
    { content_type: 'video/mp4', bitrate: 1400, url: 'https://video.test/high.mp4' },
  ] })
  assert.equal(variant.url, 'https://video.test/high.mp4')
})

test('videoMedia rejects videos with no usable MP4 variant', () => {
  assert.deepEqual(videoMedia({ mediaDetails: [{
    type: 'animated_gif',
    video_info: { variants: [{ content_type: 'application/x-mpegURL', url: 'https://video.test/a.m3u8' }] },
  }] }), [])
})

test('postMedia preserves mixed photo and video source order', () => {
  const post = { mediaDetails: [
    { type: 'photo', media_url_https: 'https://img.test/one.jpg', original_info: { width: 600, height: 400 } },
    { type: 'video', video_info: { variants: [{ content_type: 'video/mp4', url: 'https://video.test/two.mp4' }] } },
  ] }
  assert.deepEqual(postMedia(post).map(({ kind, url }) => [kind, url]), [
    ['image', 'https://img.test/one.jpg'],
    ['video', 'https://video.test/two.mp4'],
  ])
})
