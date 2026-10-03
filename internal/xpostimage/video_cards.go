package xpostimage

import (
	"context"
	"fmt"
	"image"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/fogleman/gg"
	"imagepadserver/internal/xpostmodel"
)

type videoAvatars struct {
	post, quoted image.Image
}

var videoMentionPattern = regexp.MustCompile(`@[\p{L}\p{N}_]{1,30}`)

// Profile images are optional, as in the still-image renderer. One bounded
// fetch per distinct author is enough for all full-size cards and side panels.
func loadVideoAvatars(parent context.Context, post xpostmodel.Post, fetch func(context.Context, string) (image.Image, error)) videoAvatars {
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	var avatars videoAvatars
	if post.AvatarURL != "" {
		avatars.post, _ = fetch(ctx, post.AvatarURL)
	}
	if post.Quoted != nil && post.Quoted.AvatarURL != "" {
		if post.Quoted.AvatarURL == post.AvatarURL {
			avatars.quoted = avatars.post
		} else {
			avatars.quoted, _ = fetch(ctx, post.Quoted.AvatarURL)
		}
	}
	return avatars
}

func videoPostData(post xpostmodel.Post, text string) postData {
	data := postData{TweetID: post.ID, Text: text, UserName: post.Name, Handle: post.Handle, CreatedAt: post.CreatedAt}
	for _, link := range post.Links {
		data.Links = append(data.Links, postLink{URL: link.URL, ShortURL: link.ShortURL, DisplayURL: link.DisplayURL})
	}
	// The fetcher replaces attached-media t.co URLs with pic.x.com in Text.
	// Keep raw UTF-16 offsets for speech, but use those verified display aliases
	// so the still-image body formatter also omits media links in video cards.
	rawURLs := tweetURLPattern.FindAllString(post.RawText, -1)
	displayURLs := tweetURLPattern.FindAllString(post.Text, -1)
	if len(rawURLs) == len(displayURLs) && tweetURLPattern.ReplaceAllString(post.RawText, "") == tweetURLPattern.ReplaceAllString(post.Text, "") {
		for i, displayURL := range displayURLs {
			if isXMediaURL(displayURL) {
				data.Links = append(data.Links, postLink{URL: displayURL, ShortURL: rawURLs[i], DisplayURL: displayURL})
			}
		}
	}
	return data
}

func videoQuotedData(post xpostmodel.Post, text string) quotedPostData {
	data := videoPostData(post, text)
	return quotedPostData{Relation: post.Relation, TweetID: data.TweetID, Text: data.Text, UserName: data.UserName,
		Handle: data.Handle, CreatedAt: data.CreatedAt, Links: data.Links}
}

func videoPostText(post xpostmodel.Post) string {
	if post.RawText != "" {
		return post.RawText
	}
	return post.Text
}

type videoCardGeometry struct {
	frame, quote     image.Rectangle
	header, body     image.Point
	bodyWidth, bodyH float64
	bodyText         string
	maxFont, minFont int
	quoteScale       float64
	quoteData        *quotedPostData
}

func layoutVideoCard(width, height int, parent, current xpostmodel.Post, text string, quoted bool, fonts *fontSource) (videoCardGeometry, error) {
	dc := gg.NewContext(1, 1)
	frameWidth := min(width-64, 1440)
	padding, headerHeight, gap := 44, 168, 32
	maxHeight := height - 152
	g := videoCardGeometry{bodyWidth: float64(frameWidth - padding*2), bodyText: text, maxFont: 52, minFont: 44, quoteScale: 1.5}
	if width < 1200 {
		g.maxFont, g.minFont, g.quoteScale = 42, 36, 1.25
	}
	availableBody := float64(maxHeight - padding*2 - headerHeight)
	quoteFont := int(math.Round(28 * g.quoteScale))
	quoteBodyWidth := g.bodyWidth - math.Round(48*g.quoteScale)
	quoteHeight := 0
	if quoted {
		// The parent remains visible above the quote; only this contextual preview
		// may be abbreviated. Every narrated chunk is rendered in full below it.
		data := videoPostData(parent, videoPostText(parent))
		preview, err := videoTextPreview(dc, fonts, data, g.bodyWidth, 36, 2)
		if err != nil {
			return g, err
		}
		g.bodyText, g.maxFont, g.minFont = preview, 36, 36
		bodyH, err := measureTweetBodyHeight(dc, fonts, preview, nil, "", "", g.bodyWidth, availableBody, 36, 36)
		if err != nil {
			return g, err
		}
		g.bodyH = bodyH
		quoteHeight = int(availableBody - bodyH - float64(gap))
		dataQuote := videoQuotedData(current, text)
		if _, err := measureTweetBodyHeight(dc, fonts, dataQuote.Text, dataQuote.Links, "", "", quoteBodyWidth,
			float64(quoteHeight)-math.Round(144*g.quoteScale), quoteFont, quoteFont); err != nil {
			return g, err
		}
		g.quoteData = &dataQuote
	} else {
		if parent.Quoted != nil {
			quoteHeight = int(math.Ceil(g.quoteScale*(144+3*28*1.35))) + 8
			availableBody -= float64(quoteHeight + gap)
			dataQuote := videoQuotedData(*parent.Quoted, videoPostText(*parent.Quoted))
			preview, err := videoTextPreview(dc, fonts, videoPostData(*parent.Quoted, dataQuote.Text), quoteBodyWidth, quoteFont, 3)
			if err != nil {
				return g, err
			}
			dataQuote.Text, dataQuote.Links = preview, nil
			g.quoteData = &dataQuote
		}
		data := videoPostData(current, text)
		bodyH, err := measureTweetBodyHeight(dc, fonts, text, data.Links, "", "", g.bodyWidth, availableBody, g.maxFont, g.minFont)
		if err != nil {
			return g, err
		}
		g.bodyH = bodyH
	}
	frameHeight := padding*2 + headerHeight + int(math.Ceil(g.bodyH))
	if g.quoteData != nil {
		frameHeight += gap + quoteHeight
	}
	x, y := (width-frameWidth)/2, max(48, (height-64-frameHeight)/2)
	g.frame = image.Rect(x, y, x+frameWidth, y+frameHeight)
	g.header = image.Pt(x+padding, y+padding)
	g.body = image.Pt(g.header.X, g.header.Y+headerHeight)
	if g.quoteData != nil {
		quoteY := g.body.Y + int(math.Ceil(g.bodyH)) + gap
		g.quote = image.Rect(g.body.X, quoteY, g.body.X+int(g.bodyWidth), quoteY+quoteHeight)
	}
	return g, nil
}

// Reuse the still-image URL formatting for previews as well as full bodies.
// Abbreviation is restricted to the inactive post/quote, never narrated text.
func videoTextPreview(dc *gg.Context, fonts *fontSource, data postData, width float64, size, maxLines int) (string, error) {
	text, urls := prepareTweetContent(data.Text, data.Links, "", "")
	if len(urls) > 0 {
		text = strings.TrimSpace(text + "\n" + strings.Join(urls, "\n"))
	}
	face, err := fonts.face(float64(size))
	if err != nil {
		return "", err
	}
	defer face.Close()
	dc.SetFontFace(face)
	lines := wrapText(dc, text, width)
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		runes := []rune(lines[maxLines-1])
		for len(runes) > 0 {
			if w, _ := dc.MeasureString(string(runes) + "…"); w <= width {
				break
			}
			runes = runes[:len(runes)-1]
		}
		lines[maxLines-1] = string(runes) + "…"
	}
	return strings.Join(lines, "\n"), nil
}

// Cards and panels share page boundaries, selected against both actual font
// layouts. UTF-16 entities and surrogate pairs stay intact for narration.
func paginateVideoCardText(parent, current xpostmodel.Post, text string, quoted bool, fonts *fontSource) ([]videoTextChunk, error) {
	entities := append([]xpostmodel.Entity(nil), current.Entities...)
	// Public responses sometimes lack entity indices (including the real-source
	// regression post). Protect recognizable tokens as a whole so layout splits
	// cannot expose half a URL or handle to the narration fallback matcher.
	for _, pattern := range []*regexp.Regexp{tweetURLPattern, videoMentionPattern} {
		for _, match := range pattern.FindAllStringIndex(text, -1) {
			start := len(utf16.Encode([]rune(text[:match[0]])))
			end := start + len(utf16.Encode([]rune(text[match[0]:match[1]])))
			entities = append(entities, xpostmodel.Entity{Start: start, End: end, Kind: "pagination_token"})
		}
	}
	pending := splitVideoText(text, entities, 360)
	var pages []videoTextChunk
	for len(pending) > 0 {
		chunk := pending[0]
		pending = pending[1:]
		var fitErr error
		for _, width := range []int{1920, 960} {
			if _, err := layoutVideoCard(width, 1080, parent, current, chunk.text, quoted, fonts); err != nil {
				fitErr = err
				break
			}
		}
		if fitErr == nil {
			pages = append(pages, chunk)
			continue
		}
		units := len(utf16.Encode([]rune(chunk.text)))
		if units < 2 {
			return nil, fmt.Errorf("layout X video post: %w", fitErr)
		}
		split := splitVideoText(chunk.text, chunk.entities, units/2)
		if len(split) < 2 {
			return nil, fmt.Errorf("X video text entity exceeds card bounds: %w", fitErr)
		}
		pending = append(split, pending...)
	}
	return pages, nil
}

func renderVideoCard(path string, width, height int, parent, current xpostmodel.Post, text string, quoted bool, palette renderPalette, fonts *fontSource, credit string, avatars videoAvatars) error {
	g, err := layoutVideoCard(width, height, parent, current, text, quoted, fonts)
	if err != nil {
		return err
	}
	dc := gg.NewContext(width, height)
	dc.SetHexColor(palette.Background)
	dc.Clear()
	drawPostFrame(dc, g.frame, palette)
	headerPost := current
	if quoted {
		headerPost = parent
	}
	if err := drawHeader(dc, fonts, palette, videoPostData(headerPost, ""), avatars.post, g.header.X, g.header.Y, g.bodyWidth); err != nil {
		return err
	}
	bodyLinks := videoPostData(current, text).Links
	if quoted {
		bodyLinks = nil
	}
	if err := drawTweetBody(dc, fonts, palette, g.bodyText, bodyLinks, "", "", g.body.X, g.body.Y, g.bodyWidth, g.bodyH+1, g.maxFont, g.minFont); err != nil {
		return err
	}
	if g.quoteData != nil {
		err := drawQuotedPostScaled(dc, fonts, palette, g.quoteData, avatars.quoted, g.quote, g.quoteScale)
		if err != nil {
			return err
		}
	}
	if err := drawSingleLine(dc, fonts, strings.TrimSpace(credit), float64(g.frame.Min.X), float64(height-30), float64(g.frame.Dx()), 24, 18, palette.SecondaryText); err != nil {
		return err
	}
	if err := dc.SavePNG(path); err != nil {
		return fmt.Errorf("write X video post card: %w", err)
	}
	return nil
}
