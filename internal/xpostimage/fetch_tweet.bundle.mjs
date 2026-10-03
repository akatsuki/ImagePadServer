/*! react-tweet 3.3.1 MIT License
 * The MIT License (MIT)
 * 
 * Copyright (c) 2023 Luis Alvarez.
 * 
 * Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated documentation files (the "Software"), to deal in the Software without restriction, including without limitation the rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the Software, and to permit persons to whom the Software is furnished to do so, subject to the following conditions:
 * 
 * The above copyright notice and this permission notice shall be included in all copies or substantial portions of the Software.
 * 
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
 */

// node_modules/react-tweet/dist/api/fetch-tweet.js
var SYNDICATION_URL = "https://cdn.syndication.twimg.com";
var TwitterApiError = class extends Error {
  constructor({ message, status, data }) {
    super(message);
    this.name = "TwitterApiError";
    this.status = status;
    this.data = data;
  }
};
var TWEET_ID = /^[0-9]+$/;
function getToken(id2) {
  return (Number(id2) / 1e15 * Math.PI).toString(6 ** 2).replace(/(0+|\.)/g, "");
}
async function fetchTweet(id2, fetchOptions) {
  var _res_headers_get;
  if (id2.length > 40 || !TWEET_ID.test(id2)) {
    throw new Error(`Invalid tweet id: ${id2}`);
  }
  const url = new URL(`${SYNDICATION_URL}/tweet-result`);
  url.searchParams.set("id", id2);
  url.searchParams.set("lang", "en");
  url.searchParams.set("features", [
    "tfw_timeline_list:",
    "tfw_follower_count_sunset:true",
    "tfw_tweet_edit_backend:on",
    "tfw_refsrc_session:on",
    "tfw_fosnr_soft_interventions_enabled:on",
    "tfw_show_birdwatch_pivots_enabled:on",
    "tfw_show_business_verified_badge:on",
    "tfw_duplicate_scribes_to_settings:on",
    "tfw_use_profile_image_shape_enabled:on",
    "tfw_show_blue_verified_badge:on",
    "tfw_legacy_timeline_sunset:true",
    "tfw_show_gov_verified_badge:on",
    "tfw_show_business_affiliate_badge:on",
    "tfw_tweet_edit_frontend:on"
  ].join(";"));
  url.searchParams.set("token", getToken(id2));
  const res = await fetch(url.toString(), fetchOptions);
  const isJson = (_res_headers_get = res.headers.get("content-type")) == null ? void 0 : _res_headers_get.includes("application/json");
  const data = isJson ? await res.json() : void 0;
  if (res.ok) {
    if ((data == null ? void 0 : data.__typename) === "TweetTombstone") {
      return {
        tombstone: true
      };
    }
    if (data && Object.keys(data).length === 0) {
      return {
        notFound: true
      };
    }
    return {
      data
    };
  }
  if (res.status === 404) {
    return {
      notFound: true
    };
  }
  throw new TwitterApiError({
    message: typeof data.error === "string" ? data.error : `Failed to fetch tweet at "${url}" with "${res.status}".`,
    status: res.status,
    data
  });
}

// tweet_media.mjs
function stillImages(post) {
  if (!post) return [];
  const isPhotoAssetURL = (rawURL) => {
    try {
      const parsed = new URL(rawURL);
      const host = parsed.hostname.toLowerCase().replace(/^www\./, "");
      return ["http:", "https:"].includes(parsed.protocol) && !["t.co", "pic.x.com", "pic.twitter.com", "x.com", "twitter.com"].includes(host);
    } catch {
      return false;
    }
  };
  const mediaDetails = Array.isArray(post.mediaDetails) ? post.mediaDetails : [];
  const entityMedia = Array.isArray(post.entities?.media) ? post.entities.media : [];
  const postPhotos = Array.isArray(post.photos) ? post.photos : [];
  const toPhoto = (candidate) => {
    if (!candidate) return null;
    const url2 = candidate.url || candidate.media_url_https || "";
    const width2 = candidate.width || candidate.original_info?.width || 0;
    const height2 = candidate.height || candidate.original_info?.height || 0;
    return isPhotoAssetURL(url2) && width2 > 0 && height2 > 0 ? { url: url2, width: width2, height: height2 } : null;
  };
  const stillPhotos = [];
  const seenPhotoURLs = /* @__PURE__ */ new Set();
  const photoCandidates = [
    ...postPhotos,
    ...mediaDetails.filter((media) => media.type === "photo"),
    ...entityMedia.filter((media) => media.type === "photo")
  ];
  for (const candidate of photoCandidates) {
    const photo = toPhoto(candidate);
    if (!photo || seenPhotoURLs.has(photo.url)) continue;
    stillPhotos.push(photo);
    seenPhotoURLs.add(photo.url);
    if (stillPhotos.length === 4) break;
  }
  if (stillPhotos.length > 0) return stillPhotos;
  const videoMedia = [...mediaDetails, ...entityMedia].find((media) => media.type === "video" || media.type === "animated_gif");
  const video = post.video ?? {};
  const url = video.poster || videoMedia?.media_url_https || "";
  if (!url) return [];
  const ratio = video.aspectRatio || videoMedia?.video_info?.aspect_ratio || [];
  const width = videoMedia?.original_info?.width || ratio[0] || 0;
  const height = videoMedia?.original_info?.height || ratio[1] || 0;
  return [{ url, width, height }];
}

// video_media.mjs
var maxVideoBytes = 4 * 1024 ** 3 - 1;
function isHTTPURL(rawURL) {
  try {
    const parsed = new URL(rawURL);
    return (parsed.protocol === "https:" || parsed.protocol === "http:") && Boolean(parsed.hostname);
  } catch {
    return false;
  }
}
function bestMP4Variant(videoInfo) {
  const variants = Array.isArray(videoInfo?.variants) ? videoInfo.variants : [];
  const valid = variants.filter((variant) => {
    const contentType = String(variant?.content_type || "").toLowerCase();
    const url = String(variant?.url || "");
    const mp4 = contentType.includes("mp4") || /\.mp4(?:[?#]|$)/i.test(url);
    const declaredSize = Number(variant?.size ?? variant?.content_length ?? 0);
    return mp4 && isHTTPURL(url) && !(declaredSize > maxVideoBytes);
  });
  valid.sort((a, b) => Number(b.bitrate || 0) - Number(a.bitrate || 0));
  return valid[0] || null;
}
function dimension(value) {
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : 0;
}
function postMedia(post) {
  if (!post) return [];
  const details = Array.isArray(post.mediaDetails) ? post.mediaDetails : [];
  const entities = Array.isArray(post.entities?.media) ? post.entities.media : [];
  const candidates = [...details, ...entities];
  if (post.video && !candidates.includes(post.video)) candidates.push(post.video);
  const result = [];
  const seen = /* @__PURE__ */ new Set();
  for (const candidate of candidates) {
    const type = candidate?.type;
    let asset;
    if (type === "photo") {
      const url = candidate.media_url_https || candidate.media_url || candidate.url || "";
      if (!isHTTPURL(url)) continue;
      const original = candidate.original_info || {};
      asset = {
        kind: "image",
        url,
        width: dimension(original.width || candidate.width),
        height: dimension(original.height || candidate.height)
      };
    } else if (type === "video" || type === "animated_gif" || candidate === post.video) {
      const info = candidate.video_info || candidate.videoInfo || post.video || {};
      const variant = bestMP4Variant(info);
      if (!variant) continue;
      const duration = Number(info.duration_millis ?? candidate.duration_millis ?? 0);
      asset = {
        kind: type === "animated_gif" ? "animated_gif" : "video",
        url: variant.url,
        width: 0,
        height: 0,
        duration: Number.isFinite(duration) && duration > 0 ? duration / 1e3 : 0,
        hasAudio: Boolean(info.has_audio ?? candidate.has_audio)
      };
    } else {
      continue;
    }
    const identity = candidate.id_str || candidate.id || candidate.media_url_https || asset.url;
    if (seen.has(identity)) continue;
    seen.add(identity);
    result.push(asset);
    if (result.length === 4) break;
  }
  return result;
}

// fetch_tweet.mjs
var id = process.argv[2];
if (!id || !/^\d{1,40}$/.test(id)) {
  console.error("A numeric X post ID is required.");
  process.exit(2);
}
try {
  const result = await fetchTweet(id);
  if (result.tombstone) {
    throw new Error("The post is private or has been removed.");
  }
  if (result.notFound || !result.data) {
    throw new Error("The post was not found.");
  }
  const tweet = result.data;
  const user = tweet.user ?? {};
  const sourcePost = tweet.quoted_tweet || tweet.retweeted_status || null;
  const sourceUser = sourcePost?.user ?? {};
  let text = tweet.text || "";
  const noteTweet = tweet.note_tweet?.note_tweet_results?.result;
  const rawText = noteTweet?.text || tweet.text || "";
  const sourceRawText = sourcePost?.note_tweet?.note_tweet_results?.result?.text || sourcePost?.text || sourcePost?.full_text || "";
  const mediaDetails = Array.isArray(tweet.mediaDetails) ? tweet.mediaDetails : [];
  const entityMedia = Array.isArray(tweet.entities?.media) ? tweet.entities.media : [];
  const photos = stillImages(tweet);
  const photo = photos[0] ?? null;
  const mediaShortUrls = new Set([...mediaDetails, ...entityMedia].filter((media) => media.url).map((media) => media.url));
  const sourceMediaDetails = Array.isArray(sourcePost?.mediaDetails) ? sourcePost.mediaDetails : [];
  const sourceEntityMedia = Array.isArray(sourcePost?.entities?.media) ? sourcePost.entities.media : [];
  const sourcePhotos = stillImages(sourcePost);
  const sourcePhoto = sourcePhotos[0] ?? null;
  const entityUrls = Array.isArray(tweet.entities?.urls) ? tweet.entities.urls : [];
  const sourceEntityUrls = Array.isArray(sourcePost?.entities?.urls) ? sourcePost.entities.urls : [];
  const sourceMediaShortUrls = new Set([...sourceMediaDetails, ...sourceEntityMedia].filter((media) => media.url).map((media) => media.url));
  const hasVideoMedia = (post) => Boolean(post?.video) || [...post?.mediaDetails ?? [], ...post?.entities?.media ?? []].some((media) => media.type === "video" || media.type === "animated_gif");
  const textEntities = (post, rawText2) => {
    const note = post?.note_tweet?.note_tweet_results?.result;
    const entitySet = note?.entity_set || post?.entities || {};
    const utf16Offsets = [0];
    for (const character of rawText2) {
      utf16Offsets.push(utf16Offsets.at(-1) + character.length);
    }
    const entities = [];
    for (const [collection, kind] of [["urls", "url"], ["user_mentions", "mention"]]) {
      for (const entity of Array.isArray(entitySet[collection]) ? entitySet[collection] : []) {
        const [start, end] = Array.isArray(entity.indices) ? entity.indices : [];
        if (Number.isSafeInteger(start) && Number.isSafeInteger(end) && start >= 0 && end > start) {
          if (end >= utf16Offsets.length) {
            throw new Error(`invalid Unicode code-point entity range ${start}:${end} for text length ${utf16Offsets.length - 1}`);
          }
          entities.push({ start: utf16Offsets[start], end: utf16Offsets[end], kind });
        }
      }
    }
    return entities.sort((a, b) => a.start - b.start || a.end - b.end);
  };
  const hasLinkedTweetMedia = (urls) => urls.some(
    (link) => /\/status\/\d+\/(?:photo|video)\/\d+\/?(?:[?#].*)?$/.test(link.expanded_url || "")
  );
  const mediaLinkWithoutPoster = !photo && (hasVideoMedia(tweet) || hasLinkedTweetMedia(entityUrls));
  const sourceMediaLinkWithoutPoster = !sourcePhoto && (hasVideoMedia(sourcePost) || hasLinkedTweetMedia(sourceEntityUrls));
  const links = entityUrls.map((link) => ({
    url: link.expanded_url || link.url || "",
    shortUrl: link.url || "",
    displayUrl: link.display_url || link.expanded_url || link.url || ""
  })).filter((link) => link.url && !mediaShortUrls.has(link.shortUrl));
  for (const link of links) {
    if (link.shortUrl && link.displayUrl && link.shortUrl !== link.displayUrl) {
      text = text.replaceAll(link.shortUrl, link.displayUrl);
    }
  }
  for (const media of [...mediaDetails, ...entityMedia]) {
    if (media.url && media.display_url) {
      text = text.replaceAll(media.url, media.display_url);
    }
  }
  const knownLinks = new Set(links.map((link) => link.url));
  for (const match of text.matchAll(/https?:\/\/t\.co\/[A-Za-z0-9]+/g)) {
    if (!knownLinks.has(match[0]) && !mediaShortUrls.has(match[0])) {
      links.push({ url: match[0], shortUrl: match[0], displayUrl: match[0] });
      knownLinks.add(match[0]);
    }
  }
  if (tweet.note_tweet && text && !/[.…。]$/.test(text)) text += "\u2026";
  let sourceText = sourcePost?.text || sourcePost?.full_text || "";
  const sourceLinks = sourceEntityUrls.map((link) => ({
    url: link.expanded_url || link.url || "",
    shortUrl: link.url || "",
    displayUrl: link.display_url || link.expanded_url || link.url || ""
  })).filter((link) => link.url && !sourceMediaShortUrls.has(link.shortUrl));
  for (const link of sourceLinks) {
    if (link.shortUrl && link.displayUrl && link.shortUrl !== link.displayUrl) {
      sourceText = sourceText.replaceAll(link.shortUrl, link.displayUrl);
    }
  }
  for (const media of [...sourceMediaDetails, ...sourceEntityMedia]) {
    if (media.url && media.display_url) {
      sourceText = sourceText.replaceAll(media.url, media.display_url);
    }
  }
  if (sourcePost?.note_tweet && sourceText && !/[.…。]$/.test(sourceText)) sourceText += "\u2026";
  const quoted = sourcePost ? {
    relation: tweet.quoted_tweet ? "quote" : "retweet",
    tweetId: sourcePost.id_str || "",
    text: sourceText,
    rawText: sourceRawText,
    entities: textEntities(sourcePost, sourceRawText),
    media: postMedia(sourcePost),
    videoExpected: hasVideoMedia(sourcePost),
    truncated: Boolean(sourcePost.truncated && !sourcePost.note_tweet?.note_tweet_results?.result?.text),
    createdAt: sourcePost.created_at || "",
    userName: sourceUser.name || sourceUser.screen_name || "Unknown user",
    handle: sourceUser.screen_name || "",
    avatarUrl: sourceUser.profile_image_url_https || sourceUser.profile_image_url || "",
    photo: sourcePhoto,
    photos: sourcePhotos,
    mediaLinkWithoutPoster: sourceMediaLinkWithoutPoster,
    links: sourceLinks
  } : null;
  process.stdout.write(JSON.stringify({
    tweetId: tweet.id_str || id,
    text,
    rawText,
    entities: textEntities(tweet, rawText),
    truncated: Boolean(tweet.truncated && !noteTweet?.text),
    media: postMedia(tweet),
    videoExpected: hasVideoMedia(tweet),
    createdAt: tweet.created_at || "",
    userName: user.name || user.screen_name || "Unknown user",
    handle: user.screen_name || "",
    avatarUrl: user.profile_image_url_https || user.profile_image_url || "",
    photo,
    photos,
    mediaLinkWithoutPoster,
    quoted,
    links
  }));
} catch (error) {
  console.error(error instanceof Error ? error.message : String(error));
  process.exit(1);
}
