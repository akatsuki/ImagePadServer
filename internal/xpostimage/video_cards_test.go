package xpostimage

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"imagepadserver/internal/xpostmodel"
	"imagepadserver/internal/xposttts"
)

func TestVideoCardsUseStillImageQuoteBoxAndPreserveParent(t *testing.T) {
	font := filepath.Join("..", "video", "fonts", "NotoSansJP-Regular.ttf")
	post := xpostmodel.Post{Name: "元の投稿者", Handle: "original", RawText: "元の投稿本文", Quoted: &xpostmodel.Post{
		Name: "引用された投稿者", Handle: "quoted", RawText: "引用された本文", Relation: "quote",
	}}
	pages, err := RenderVideoCards(post, "dark", font, t.TempDir(), "VOICEVOX:四国めたん")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || pages[0].SpeechText != "元の投稿本文" || pages[1].SpeechText != "引用された本文" {
		t.Fatalf("narration order or metadata leaked into speech: %+v", pages)
	}
	for _, page := range pages {
		for _, path := range []string{page.CardPath, page.PanelPath} {
			img := readVideoCard(t, path)
			panelPixels := 0
			for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
				for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
					if color.NRGBAModel.Convert(img.At(x, y)) == (color.NRGBA{R: 29, G: 29, B: 29, A: 255}) {
						panelPixels++
					}
				}
			}
			if panelPixels < 10000 {
				t.Fatalf("%s lacks the still-image quote box background (%d pixels)", path, panelPixels)
			}
		}
	}
	post.Name = "別の元投稿者"
	post.RawText = "別の元投稿の本文"
	changed, err := RenderVideoCards(post, "dark", font, t.TempDir(), "VOICEVOX:四国めたん")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(pages[1].PanelPath)
	after, _ := os.ReadFile(changed[1].PanelPath)
	if bytes.Equal(before, after) {
		t.Fatal("quote narration page lost the original post context")
	}
}

func TestVideoCardsDisplayFetchedPostDate(t *testing.T) {
	font := filepath.Join("..", "video", "fonts", "NotoSansJP-Regular.ttf")
	render := func(date string) []byte {
		t.Helper()
		var post xpostmodel.Post
		if err := json.Unmarshal([]byte(`{"userName":"投稿者","handle":"author","rawText":"本文","createdAt":"`+date+`"}`), &post); err != nil {
			t.Fatal(err)
		}
		pages, err := RenderVideoCards(post, "light", font, t.TempDir(), "")
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(pages[0].PanelPath)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	if bytes.Equal(render("2026-10-02T03:04:00Z"), render("2026-10-03T05:06:00Z")) {
		t.Fatal("fetched createdAt was discarded or not displayed")
	}
}

func readVideoCard(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestVideoCardsRenderBothAuthorAvatarsAndFetchEachOnce(t *testing.T) {
	post := xpostmodel.Post{Name: "元投稿者", RawText: "元本文", AvatarURL: "https://example.test/main.png",
		Quoted: &xpostmodel.Post{Name: "引用投稿者", RawText: "引用本文", AvatarURL: "https://example.test/quote.png"}}
	main := solidVideoAvatar(color.NRGBA{R: 240, G: 20, B: 70, A: 255})
	quote := solidVideoAvatar(color.NRGBA{R: 20, G: 210, B: 130, A: 255})
	calls := make(map[string]int)
	avatars := loadVideoAvatars(t.Context(), post, func(_ context.Context, url string) (image.Image, error) {
		calls[url]++
		if url == post.AvatarURL {
			return main, nil
		}
		return quote, nil
	})
	font := filepath.Join("..", "video", "fonts", "NotoSansJP-Regular.ttf")
	pages, err := renderVideoCards(post, "light", font, t.TempDir(), "", avatars)
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range pages {
		for _, path := range []string{page.CardPath, page.PanelPath} {
			img := readVideoCard(t, path)
			counts := make(map[color.NRGBA]int)
			for y := 0; y < img.Bounds().Dy(); y++ {
				for x := 0; x < img.Bounds().Dx(); x++ {
					c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
					if c == main.NRGBAAt(0, 0) || c == quote.NRGBAAt(0, 0) {
						counts[c]++
					}
				}
			}
			if counts[main.NRGBAAt(0, 0)] < 1000 || counts[quote.NRGBAAt(0, 0)] < 1000 {
				t.Fatalf("missing author avatar in %s: %v", path, counts)
			}
		}
	}
	if calls[post.AvatarURL] != 1 || calls[post.Quoted.AvatarURL] != 1 {
		t.Fatalf("avatars fetched per page: %v", calls)
	}
	post.Quoted.AvatarURL = post.AvatarURL
	calls = make(map[string]int)
	avatars = loadVideoAvatars(t.Context(), post, func(_ context.Context, url string) (image.Image, error) {
		calls[url]++
		return main, nil
	})
	if calls[post.AvatarURL] != 1 || avatars.post != avatars.quoted {
		t.Fatal("same-author avatar was not reused")
	}
}

func TestVideoBodyUsesStillImageLinkFormatting(t *testing.T) {
	post := xpostmodel.Post{RawText: "本文 https://t.co/media https://t.co/site", Text: "本文 pic.x.com/media example.test/page",
		Links: []xpostmodel.Link{{URL: "https://example.test/page", ShortURL: "https://t.co/site", DisplayURL: "example.test/page"}}}
	data := videoPostData(post, post.RawText)
	body, links := prepareTweetContent(data.Text, data.Links, "", "")
	if body != "本文" || len(links) != 1 || links[0] != "example.test/page" {
		t.Fatalf("video body differs from still-image formatting: %q %v", body, links)
	}
}

func TestVideoCardPaginationPreservesAllNarratedText(t *testing.T) {
	font := filepath.Join("..", "video", "fonts", "NotoSansJP-Regular.ttf")
	post := xpostmodel.Post{Name: "投稿者", RawText: strings.Repeat("段落の本文です。😀\n", 50),
		Quoted: &xpostmodel.Post{Name: "引用投稿者", RawText: strings.Repeat("引用の本文です。\n", 40)}}
	pages, err := RenderVideoCards(post, "light", font, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	var mainSpeech, quoteSpeech []string
	for _, page := range pages {
		if page.Quoted {
			quoteSpeech = append(quoteSpeech, page.SpeechText)
		} else {
			mainSpeech = append(mainSpeech, page.SpeechText)
		}
	}
	for _, pair := range [][2]string{{strings.Join(mainSpeech, ""), post.RawText}, {strings.Join(quoteSpeech, ""), post.Quoted.RawText}} {
		if strings.Join(strings.Fields(pair[0]), "") != strings.Join(strings.Fields(pair[1]), "") {
			t.Fatal("layout pagination dropped or changed narrated body text")
		}
	}
}

func TestVideoCardPaginationDoesNotNarrateSplitURLWithoutEntities(t *testing.T) {
	font := filepath.Join("..", "video", "fonts", "NotoSansJP-Regular.ttf")
	post := xpostmodel.Post{Name: "投稿者", RawText: strings.Repeat("あ", 500) + " https://example.test/" + strings.Repeat("URLtoken", 14) + " @author_handle " + strings.Repeat("い", 500)}
	pages, err := RenderVideoCards(post, "light", font, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	var spoken strings.Builder
	for _, page := range pages {
		spoken.WriteString(page.SpeechText)
	}
	want, err := xposttts.BuildSpeechText(post)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(strings.Fields(spoken.String()), "") != strings.Join(strings.Fields(want), "") {
		t.Fatal("URL/handle fragments leaked into narration after font-aware pagination")
	}
}

func solidVideoAvatar(c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 80, 80))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: c}, image.Point{}, draw.Src)
	return img
}

// Opt-in visual artifacts: real cached post plus a clearly separate quote
// fixture, rendered with the production helpers in both supported themes.
func TestVideoCardVisualPreview(t *testing.T) {
	dir := os.Getenv("XPOST_CARD_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set XPOST_CARD_PREVIEW_DIR to save visual comparison images")
	}
	font := filepath.Join("..", "video", "fonts", "NotoSansJP-Regular.ttf")
	var post xpostmodel.Post
	data, err := os.ReadFile(os.Getenv("XPOST_CARD_PREVIEW_POST"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &post); err != nil {
		t.Fatal(err)
	}
	avatars := loadVideoAvatars(t.Context(), post, downloadImage)
	if avatars.post == nil {
		t.Fatal("real post avatar unavailable for visual verification")
	}
	fixture := xpostmodel.Post{Name: "投稿表示の確認用", Handle: "layout_preview", CreatedAt: "2026-10-03T03:04:00Z",
		RawText: "投稿本文を読みやすく表示します。\n静止画版と同じアイコン、投稿日時、引用ボックスを使い、縦の写真や動画は左、投稿内容は右に配置します。",
		Quoted: &xpostmodel.Post{Name: "引用ボックスの確認用", Handle: "quote_preview", Relation: "quote", CreatedAt: "2026-10-02T07:08:00Z",
			RawText: "これは引用投稿の表示を確認するためのサンプルです。\n引用元のプロフィールと本文を枠の中に表示します。"}}
	for _, theme := range []string{"light", "dark"} {
		if _, err := renderVideoCards(post, theme, font, filepath.Join(dir, "real-"+theme), "VOICEVOX: 四国めたん", avatars); err != nil {
			t.Fatal(err)
		}
		fixtureAvatars := videoAvatars{solidVideoAvatar(color.NRGBA{R: 62, G: 143, B: 201, A: 255}), solidVideoAvatar(color.NRGBA{R: 198, G: 142, B: 92, A: 255})}
		if _, err := renderVideoCards(fixture, theme, font, filepath.Join(dir, "quote-"+theme), "VOICEVOX: 四国めたん", fixtureAvatars); err != nil {
			t.Fatal(err)
		}
		stillData := videoPostData(fixture, videoPostText(fixture))
		quoted := videoQuotedData(*fixture.Quoted, videoPostText(*fixture.Quoted))
		stillData.Quoted = &quoted
		still, err := renderPost(stillData, nil, fixtureAvatars.post, nil, fixtureAvatars.quoted, theme, font)
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(filepath.Join(dir, "still-reference-"+theme+".png"))
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(f, still)
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("save still reference: %v %v", err, closeErr)
		}
	}
}
