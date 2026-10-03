import test from 'node:test'
import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'

const fetchScript = fileURLToPath(new URL('./fetch_tweet.mjs', import.meta.url))
const id = '2106219507274731847'
const photos = [{ url: 'https://pbs.twimg.com/media/fixture.jpg', width: 100, height: 100 }]

// Exercise the production CLI and react-tweet parser without a network dependency.
function fetchPost(source) {
  const preload = `globalThis.fetch = async (url) => {
    if (new URL(url).origin !== 'https://cdn.syndication.twimg.com') throw new Error('Unexpected fetch');
    return new Response(${JSON.stringify(JSON.stringify({ id_str: id, photos, ...source }))}, {
      headers: { 'content-type': 'application/json' }
    });
  }`
  return spawnSync(process.execPath, [
    '--import', `data:text/javascript,${encodeURIComponent(preload)}`, fetchScript, id,
  ], { encoding: 'utf8', timeout: 10000 })
}

function normalizedPost(source) {
  const result = fetchPost(source)
  assert.equal(result.status, 0, result.stderr || String(result.error || ''))
  return JSON.parse(result.stdout)
}

function sourceEntity(text, token) {
  const start = Array.from(text.slice(0, text.indexOf(token))).length
  return { indices: [start, start + Array.from(token).length], url: token }
}

test('reported post converts source range 122:145 to UTF-16 range 124:147', () => {
  const text = '＼今日の18:00公開！／\nまだ動画編集終わってないけど自分を追い込むために予告しておきます…！\nバーチャル空間でヤチヨに会いたいけどVRChatってなに…？という人向けに動画を作っています！本日夕方公開予定！✌️\n\n🔽芳井はじめちゃんねる🔽\nhttps://t.co/aylrLiOqtY\n\n#超かぐや姫 #ツクヨミ感謝祭 https://t.co/VVP78FaJCl'
  const token = 'https://t.co/aylrLiOqtY'
  assert.equal(text.length, 188)
  assert.equal(Array.from(text).length, 186)
  const post = normalizedPost({ text, entities: { urls: [{ indices: [122, 145], url: token }] } })
  assert.equal(post.rawText, text)
  assert.deepEqual(post.entities, [{ start: 124, end: 147, kind: 'url' }])
  assert.equal(post.rawText.slice(post.entities[0].start, post.entities[0].end), token)
})

for (const prefix of ['', '日本語\n', '😀', '😀😀🔽✌️\n']) {
  test(`URL and mention coordinates preserve body after prefix ${JSON.stringify(prefix)}`, () => {
    const url = 'https://t.co/abc'
    const mention = '@author'
    const text = `${prefix}${mention} 本文 ${url} 続き😀`
    const post = normalizedPost({ text, entities: {
      urls: [sourceEntity(text, url)], user_mentions: [sourceEntity(text, mention)],
    } })
    assert.deepEqual(post.entities, [
      { start: text.indexOf(mention), end: text.indexOf(mention) + mention.length, kind: 'mention' },
      { start: text.indexOf(url), end: text.indexOf(url) + url.length, kind: 'url' },
    ])
    assert.equal(post.rawText, text)
  })
}

test('quoted post uses its own text and emoji offsets', () => {
  const text = '😀🔽引用本文 https://t.co/quote'
  const post = normalizedPost({ text: '本文😀', quoted_tweet: {
    id_str: '123', photos, full_text: text, entities: { urls: [sourceEntity(text, 'https://t.co/quote')] },
  } })
  assert.equal(post.quoted.rawText, text)
  assert.deepEqual(post.quoted.entities, [{ start: 9, end: text.length, kind: 'url' }])
  assert.deepEqual(post.entities, [])
})

test('long-form note converts entities against the full note text', () => {
  const text = '😀長い本文 @author\n🔽https://t.co/note'
  const post = normalizedPost({ text: '短い本文', entities: { urls: [] }, note_tweet: {
    note_tweet_results: { result: { text, entity_set: {
      urls: [sourceEntity(text, 'https://t.co/note')],
      user_mentions: [sourceEntity(text, '@author')],
    } } },
  } })
  assert.equal(post.rawText, text)
  assert.deepEqual(post.entities, [
    { start: text.indexOf('@author'), end: text.indexOf('@author') + 7, kind: 'mention' },
    { start: text.indexOf('https://'), end: text.length, kind: 'url' },
  ])
})

test('source ranges beyond the text fail instead of deleting unrelated narration', () => {
  const result = fetchPost({ text: '😀本文', entities: { urls: [{ indices: [1, 20] }] } })
  assert.equal(result.status, 1)
  assert.match(result.stderr, /invalid Unicode code-point entity range 1:20/)
})
