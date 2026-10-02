package xpostimage

import (
	"fmt"
	"strings"
)

type renderPalette struct {
	Background      string
	PrimaryText     string
	SecondaryText   string
	Divider         string
	Panel           string
	PanelBorder     string
	Placeholder     string
	PlaceholderText string
	Link            string
}

func paletteForTheme(mode string) (renderPalette, error) {
	light := renderPalette{
		Background:      "#FFFFFF",
		PrimaryText:     "#0F1419",
		SecondaryText:   "#536471",
		Divider:         "#DCE2E7",
		Panel:           "#F7F9FA",
		PanelBorder:     "#DCE2E7",
		Placeholder:     "#DCE8F0",
		PlaceholderText: "#1D4E70",
		Link:            "#1D9BF0",
	}
	dark := renderPalette{
		Background:      "#000000",
		PrimaryText:     "#F5F5F7",
		SecondaryText:   "#98989A",
		Divider:         "#3B3B3C",
		Panel:           "#1D1D1D",
		PanelBorder:     "#3B3B3C",
		Placeholder:     "#1D1D1D",
		PlaceholderText: "#F5F5F7",
		Link:            "#0A63C7",
	}

	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "light":
		return light, nil
	case "dark":
		return dark, nil
	default:
		return renderPalette{}, fmt.Errorf("unsupported theme %q (use light or dark)", mode)
	}
}
