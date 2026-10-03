package xpostimage

import (
	"image/color"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"imagepadserver/internal/xpostmodel"
	"imagepadserver/internal/xposttts"
)

func TestLongVideoBodyKeepsOneFixedCardAndUsesScrollTiles(t *testing.T) {
	font := filepath.Join("..", "video", "fonts", "NotoSansJP-Regular.ttf")
	post := xpostmodel.Post{Name: "投稿者", RawText: strings.Repeat("長い本文を読み上げます。😀\n", 70)}
	pages, err := RenderVideoCards(post, "light", font, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 {
		t.Fatalf("body still paginates into %d cards", len(pages))
	}
	page := pages[0]
	want, _ := xposttts.BuildSpeechText(post)
	if page.SpeechText != want {
		t.Fatal("scrolling changed or lost narrated text")
	}
	for _, view := range []*xpostmodel.ScrollView{page.CardScroll, page.PanelScroll} {
		if view == nil || view.ContentHeight <= view.Height || len(view.Tiles) < 2 {
			t.Fatalf("missing tiled overflowing body: %+v", view)
		}
		if view.Width <= 0 || view.Width > 1352 {
			t.Fatal("body exceeds its fixed width")
		}
		bottom := 0
		for _, tile := range view.Tiles {
			if tile.Top != bottom || tile.Height <= 0 || tile.Height > 1024 {
				t.Fatalf("invalid tile boundary: %+v", tile)
			}
			img := readVideoCard(t, tile.Path)
			if img.Bounds().Dx() != view.Width || img.Bounds().Dy() != tile.Height {
				t.Fatal("tile dimensions differ from metadata")
			}
			bottom += tile.Height
		}
		if bottom != view.ContentHeight {
			t.Fatal("tiles lost part of the displayed body")
		}
		if len(view.Lines) == 0 || view.Lines[len(view.Lines)-1].EndRune != utf8.RuneCountInString(want) {
			t.Fatal("scroll mapping does not reach the end of narration")
		}
	}
}

func TestShortAndLongVideoCardsHaveTheSameFixedFrame(t *testing.T) {
	font := filepath.Join("..", "video", "fonts", "NotoSansJP-Regular.ttf")
	for _, theme := range []string{"light", "dark"} {
		short, err := RenderVideoCards(xpostmodel.Post{Name: "投稿者", RawText: "短い本文。"}, theme, font, t.TempDir(), "")
		if err != nil {
			t.Fatal(err)
		}
		long, err := RenderVideoCards(xpostmodel.Post{Name: "投稿者", RawText: strings.Repeat("長文。", 200)}, theme, font, t.TempDir(), "")
		if err != nil {
			t.Fatal(err)
		}
		if len(short) != 1 || len(long) != 1 {
			t.Fatal("long body switched cards")
		}
		for _, pair := range [][2]string{{short[0].CardPath, long[0].CardPath}, {short[0].PanelPath, long[0].PanelPath}} {
			a, b := readVideoCard(t, pair[0]), readVideoCard(t, pair[1])
			// The header and frame top/bottom remain pixel-identical across lengths.
			for _, y := range []int{75, 76, 90, 120, 200, 1003, 1004} {
				for x := 0; x < a.Bounds().Dx(); x++ {
					if color.NRGBAModel.Convert(a.At(x, y)) != color.NRGBAModel.Convert(b.At(x, y)) {
						t.Fatalf("frame/header changed at %d,%d (%s)", x, y, theme)
					}
				}
			}
		}
		if short[0].CardScroll != nil && short[0].CardScroll.ContentHeight > short[0].CardScroll.Height {
			t.Fatal("short body requires scrolling")
		}
	}
}

func TestScrollPreservesQuoteTransitionAndOmitsURLsFromSpeech(t *testing.T) {
	font := filepath.Join("..", "video", "fonts", "NotoSansJP-Regular.ttf")
	post := xpostmodel.Post{Name: "元投稿者", RawText: strings.Repeat("本文です。😀 ", 50) + "https://example.test/page @author 最後。", Quoted: &xpostmodel.Post{Name: "引用投稿者", RawText: strings.Repeat("引用本文。\n", 80)}}
	pages, err := RenderVideoCards(post, "dark", font, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || !pages[1].Quoted {
		t.Fatal("quote still uses length-dependent pages")
	}
	for i, source := range []xpostmodel.Post{post, *post.Quoted} {
		want, _ := xposttts.BuildSpeechText(source)
		if pages[i].SpeechText != want {
			t.Fatal("URL/mention leaked or body disappeared")
		}
	}
	for _, pair := range [][2]string{{pages[0].CardEndPath, pages[1].CardPath}, {pages[0].PanelEndPath, pages[1].PanelPath}} {
		if pair[0] == "" {
			t.Fatal("missing final-state snapshot")
		}
		a, b := readVideoCard(t, pair[0]), readVideoCard(t, pair[1])
		for y := 0; y < a.Bounds().Dy(); y++ {
			for x := 0; x < a.Bounds().Dx(); x++ {
				if color.NRGBAModel.Convert(a.At(x, y)) != color.NRGBAModel.Convert(b.At(x, y)) {
					t.Fatalf("quote transition changes visual state at %d,%d", x, y)
				}
			}
		}
	}
}
