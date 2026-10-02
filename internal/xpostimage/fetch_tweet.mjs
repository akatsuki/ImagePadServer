import { fetchTweet } from 'react-tweet/api'
import { stillImages } from './tweet_media.mjs'

const id = process.argv[2]

if (!id || !/^\d{1,40}$/.test(id)) {
  console.error('A numeric X post ID is required.')
  process.exit(2)
}

try {
  const result = await fetchTweet(id)
  if (result.tombstone) {
    throw new Error('The post is private or has been removed.')
  }
  if (result.notFound || !result.data) {
    throw new Error('The post was not found.')
  }

  const tweet = result.data
  const user = tweet.user ?? {}
  const sourcePost = tweet.quoted_tweet || tweet.retweeted_status || null
  const sourceUser = sourcePost?.user ?? {}
  let text = tweet.text || ''
  const mediaDetails = Array.isArray(tweet.mediaDetails) ? tweet.mediaDetails : []
  const entityMedia = Array.isArray(tweet.entities?.media) ? tweet.entities.media : []
  const photos = stillImages(tweet)
  const photo = photos[0] ?? null
  const mediaShortUrls = new Set([...mediaDetails, ...entityMedia]
    .filter((media) => media.url)
    .map((media) => media.url))
  const sourceMediaDetails = Array.isArray(sourcePost?.mediaDetails) ? sourcePost.mediaDetails : []
  const sourceEntityMedia = Array.isArray(sourcePost?.entities?.media) ? sourcePost.entities.media : []
  const sourcePhotos = stillImages(sourcePost)
  const sourcePhoto = sourcePhotos[0] ?? null
  const entityUrls = Array.isArray(tweet.entities?.urls) ? tweet.entities.urls : []
  const sourceEntityUrls = Array.isArray(sourcePost?.entities?.urls) ? sourcePost.entities.urls : []
  const sourceMediaShortUrls = new Set([...sourceMediaDetails, ...sourceEntityMedia]
    .filter((media) => media.url)
    .map((media) => media.url))
  const hasVideoMedia = (post) => Boolean(post?.video)
    || [...(post?.mediaDetails ?? []), ...(post?.entities?.media ?? [])]
      .some((media) => media.type === 'video' || media.type === 'animated_gif')
  const hasLinkedTweetMedia = (urls) => urls.some((link) =>
    /\/status\/\d+\/(?:photo|video)\/\d+\/?(?:[?#].*)?$/.test(link.expanded_url || ''),
  )
  const mediaLinkWithoutPoster = !photo && (hasVideoMedia(tweet) || hasLinkedTweetMedia(entityUrls))
  const sourceMediaLinkWithoutPoster = !sourcePhoto && (hasVideoMedia(sourcePost) || hasLinkedTweetMedia(sourceEntityUrls))
  const links = entityUrls.map((link) => ({
    url: link.expanded_url || link.url || '',
    shortUrl: link.url || '',
    displayUrl: link.display_url || link.expanded_url || link.url || '',
  })).filter((link) => link.url && !mediaShortUrls.has(link.shortUrl))
  for (const link of links) {
    if (link.shortUrl && link.displayUrl && link.shortUrl !== link.displayUrl) {
      text = text.replaceAll(link.shortUrl, link.displayUrl)
    }
  }
  for (const media of [...mediaDetails, ...entityMedia]) {
    if (media.url && media.display_url) {
      text = text.replaceAll(media.url, media.display_url)
    }
  }
  const knownLinks = new Set(links.map((link) => link.url))
  for (const match of text.matchAll(/https?:\/\/t\.co\/[A-Za-z0-9]+/g)) {
    if (!knownLinks.has(match[0]) && !mediaShortUrls.has(match[0])) {
      links.push({ url: match[0], shortUrl: match[0], displayUrl: match[0] })
      knownLinks.add(match[0])
    }
  }
  if (tweet.note_tweet && text && !/[.…。]$/.test(text)) text += '…'
  let sourceText = sourcePost?.text || sourcePost?.full_text || ''
  const sourceLinks = sourceEntityUrls.map((link) => ({
    url: link.expanded_url || link.url || '',
    shortUrl: link.url || '',
    displayUrl: link.display_url || link.expanded_url || link.url || '',
  })).filter((link) => link.url && !sourceMediaShortUrls.has(link.shortUrl))
  for (const link of sourceLinks) {
    if (link.shortUrl && link.displayUrl && link.shortUrl !== link.displayUrl) {
      sourceText = sourceText.replaceAll(link.shortUrl, link.displayUrl)
    }
  }
  for (const media of [...sourceMediaDetails, ...sourceEntityMedia]) {
    if (media.url && media.display_url) {
      sourceText = sourceText.replaceAll(media.url, media.display_url)
    }
  }
  if (sourcePost?.note_tweet && sourceText && !/[.…。]$/.test(sourceText)) sourceText += '…'

  const quoted = sourcePost ? {
    relation: tweet.quoted_tweet ? 'quote' : 'retweet',
    tweetId: sourcePost.id_str || '',
    text: sourceText,
    createdAt: sourcePost.created_at || '',
    userName: sourceUser.name || sourceUser.screen_name || 'Unknown user',
    handle: sourceUser.screen_name || '',
    avatarUrl: sourceUser.profile_image_url_https || sourceUser.profile_image_url || '',
    photo: sourcePhoto,
    photos: sourcePhotos,
    mediaLinkWithoutPoster: sourceMediaLinkWithoutPoster,
    links: sourceLinks,
  } : null

  process.stdout.write(JSON.stringify({
    tweetId: tweet.id_str || id,
    text,
    createdAt: tweet.created_at || '',
    userName: user.name || user.screen_name || 'Unknown user',
    handle: user.screen_name || '',
    avatarUrl: user.profile_image_url_https || user.profile_image_url || '',
    photo,
    photos,
    mediaLinkWithoutPoster,
    quoted,
    links,
  }))
} catch (error) {
  console.error(error instanceof Error ? error.message : String(error))
  process.exit(1)
}
