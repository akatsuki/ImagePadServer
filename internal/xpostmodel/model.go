// Package xpostmodel contains immutable values shared by X video preparation,
// narration and rendering. It has no process or settings dependencies.
package xpostmodel

type Voice struct {
	EngineURL   string  `json:"engineUrl"`
	Managed     bool    `json:"managed,omitempty"`
	StyleID     int     `json:"styleId"`
	Speed       float64 `json:"speed"`
	SpeakerUUID string  `json:"speakerUuid"`
	SpeakerName string  `json:"speakerName"`
	StyleName   string  `json:"styleName"`
}

type Entity struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Kind  string `json:"kind"`
}

type Link struct {
	URL        string `json:"url"`
	ShortURL   string `json:"shortUrl"`
	DisplayURL string `json:"displayUrl"`
}

type Post struct {
	ID        string   `json:"tweetId"`
	Text      string   `json:"text"`
	RawText   string   `json:"rawText"`
	Name      string   `json:"userName"`
	Handle    string   `json:"handle"`
	AvatarURL string   `json:"avatarUrl,omitempty"`
	CreatedAt string   `json:"createdAt,omitempty"`
	Links     []Link   `json:"links,omitempty"`
	Entities  []Entity `json:"entities,omitempty"`
	Media     []Media  `json:"media,omitempty"`
	Quoted    *Post    `json:"quoted,omitempty"`
	Relation  string   `json:"relation,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

type Media struct {
	Kind      string  `json:"kind"`
	URL       string  `json:"url"`
	Path      string  `json:"path,omitempty"`
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	Duration  float64 `json:"duration,omitempty"`
	HasAudio  bool    `json:"hasAudio,omitempty"`
	FirstPath string  `json:"firstPath,omitempty"`
	LastPath  string  `json:"lastPath,omitempty"`
}

type Page struct {
	CardPath     string      `json:"cardPath"`
	PanelPath    string      `json:"panelPath"`
	SpeechText   string      `json:"speechText"`
	Quoted       bool        `json:"quoted"`
	CardScroll   *ScrollView `json:"cardScroll,omitempty"`
	PanelScroll  *ScrollView `json:"panelScroll,omitempty"`
	CardEndPath  string      `json:"cardEndPath,omitempty"`
	PanelEndPath string      `json:"panelEndPath,omitempty"`
}

// ScrollView describes immutable body tiles in the source card's coordinates.
// Narration rune offsets refer to Page.SpeechText, not the displayed URLs/IDs.
type ScrollView struct {
	X, Y, Width, Height, ContentHeight int
	Tiles                              []ScrollTile
	Lines                              []ScrollLine
}

type ScrollTile struct {
	Path        string
	Top, Height int
}

type ScrollLine struct {
	StartRune, EndRune int
	Top, Bottom        float64
}

type SpeechCue struct {
	StartRune, EndRune       int
	StartSeconds, EndSeconds float64
}

type Speech struct {
	Path       string
	Duration   float64
	SampleRate int
	Channels   int
	Samples    int64
	Cues       []SpeechCue `json:",omitempty"`
}
