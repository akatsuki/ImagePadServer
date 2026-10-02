import assert from 'node:assert/strict'
import test from 'node:test'

import { stillImages } from './tweet_media.mjs'

test('stillImages returns an empty list when a post has no media', () => {
  assert.deepEqual(stillImages({ text: 'plain text post' }), [])
})

test('stillImages returns an empty list when video has no poster', () => {
  assert.deepEqual(stillImages({ mediaDetails: [{ type: 'video' }] }), [])
})
