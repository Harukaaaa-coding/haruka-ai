// Package voicecontrol converts a bounded, structured expression plan into
// provider-safe speech text. It deliberately owns the Fish Audio S2 control
// markers so that text supplied by a user or an LLM cannot inject arbitrary
// bracket cues.
package voicecontrol

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxTextRunes bounds one synthesis request before provider-specific limits
	// are applied by the caller.
	MaxTextRunes = 12000
	// MaxEmphasisItems and MaxCueItems limit the amount of generated control
	// syntax in a single utterance.
	MaxEmphasisItems = 8
	MaxCueItems      = 12
	// MaxAnchorRunes prevents a plan from using a whole utterance as an anchor.
	MaxAnchorRunes = 80
)

var (
	ErrEmptyText       = errors.New("voice control text is empty after sanitization")
	ErrTextTooLong     = errors.New("voice control text exceeds maximum length")
	ErrTooManyCues     = errors.New("voice control plan has too many cues")
	ErrTooManyEmphasis = errors.New("voice control plan has too many emphasis items")
)

// Emotion is the primary emotional intent of an utterance. Values outside the
// constants below are rejected rather than passed through to Fish S2.
type Emotion string

const (
	EmotionNeutral    Emotion = ""
	EmotionWarm       Emotion = "warm"
	EmotionCalm       Emotion = "calm"
	EmotionFriendly   Emotion = "friendly"
	EmotionEmpathetic Emotion = "empathetic"
	EmotionConfident  Emotion = "confident"
	EmotionCheerful   Emotion = "cheerful"
	EmotionExcited    Emotion = "excited"
	EmotionSerious    Emotion = "serious"
	EmotionSad        Emotion = "sad"
)

// Delivery describes how the utterance should be delivered independently from
// its emotional state.
type Delivery string

const (
	DeliveryNatural        Delivery = ""
	DeliveryGentle         Delivery = "gentle"
	DeliveryConversational Delivery = "conversational"
	DeliveryNarrative      Delivery = "narrative"
	DeliveryProfessional   Delivery = "professional"
	DeliveryEnergetic      Delivery = "energetic"
)

// VolumeIntent expresses a relative volume preference. It is intentionally
// qualitative because Fish S2's bracket controls are descriptive, not an
// exact dB API.
type VolumeIntent string

const (
	VolumeDefault VolumeIntent = ""
	VolumeSoft    VolumeIntent = "soft"
	VolumeNormal  VolumeIntent = "normal"
	VolumeLoud    VolumeIntent = "loud"
)

// PaceIntent expresses a relative speaking pace.
type PaceIntent string

const (
	PaceDefault PaceIntent = ""
	PaceSlow    PaceIntent = "slow"
	PaceNormal  PaceIntent = "normal"
	PaceFast    PaceIntent = "fast"
)

// CueKind is a short, provider-safe set of pauses and non-verbal effects.
// New effects must be added here and mapped below; callers cannot provide raw
// Fish S2 cue strings.
type CueKind string

const (
	CueShortPause CueKind = "short_pause"
	CueLongPause  CueKind = "long_pause"
	CueLaugh      CueKind = "laugh"
	CueSigh       CueKind = "sigh"
	CueGasp       CueKind = "gasp"
	CueInhale     CueKind = "inhale"
	CueExhale     CueKind = "exhale"
)

// CuePosition controls where a cue appears relative to an anchor. Start and
// End do not use Anchor; Before and After require one.
type CuePosition string

const (
	CueAfter  CuePosition = ""
	CueBefore CuePosition = "before"
	CueStart  CuePosition = "start"
	CueEnd    CuePosition = "end"
)

// Emphasis asks Fish S2 to emphasize a selected occurrence of Phrase. The
// first occurrence is zero. Phrase is matched against sanitized plain text.
type Emphasis struct {
	Phrase     string `json:"phrase"`
	Occurrence int    `json:"occurrence,omitempty"`
}

// Cue inserts one allowed pause or non-verbal effect. For Before and After,
// Occurrence is zero-based and refers to the matching Anchor occurrence.
type Cue struct {
	Kind       CueKind     `json:"kind"`
	Position   CuePosition `json:"position,omitempty"`
	Anchor     string      `json:"anchor,omitempty"`
	Occurrence int         `json:"occurrence,omitempty"`
}

// ExpressionPlan is provider-neutral structured speech intent. Text can come
// from any untrusted source: Render removes raw Fish-style bracket controls
// before it emits the controlled S2 form.
type ExpressionPlan struct {
	Text     string       `json:"text"`
	Emotion  Emotion      `json:"emotion,omitempty"`
	Delivery Delivery     `json:"delivery,omitempty"`
	Volume   VolumeIntent `json:"volume,omitempty"`
	Pace     PaceIntent   `json:"pace,omitempty"`
	Emphasis []Emphasis   `json:"emphasis,omitempty"`
	Cues     []Cue        `json:"cues,omitempty"`
}

// Rendered contains both a provider-agnostic value and Fish Audio S2-specific
// text. Callers for a provider without expression-tag support should use
// PlainText; it never contains generated or user-provided bracket controls.
type Rendered struct {
	PlainText  string
	FishS2Text string
}

var emotionTags = map[Emotion]string{
	EmotionNeutral:    "",
	EmotionWarm:       "warm",
	EmotionCalm:       "calm",
	EmotionFriendly:   "friendly",
	EmotionEmpathetic: "empathetic",
	EmotionConfident:  "confident",
	EmotionCheerful:   "cheerful",
	EmotionExcited:    "excited",
	EmotionSerious:    "serious",
	EmotionSad:        "sad",
}

var deliveryTags = map[Delivery]string{
	DeliveryNatural:        "",
	DeliveryGentle:         "gentle",
	DeliveryConversational: "conversational",
	DeliveryNarrative:      "narrative",
	DeliveryProfessional:   "professional",
	DeliveryEnergetic:      "energetic",
}

var volumeTags = map[VolumeIntent]string{
	VolumeDefault: "",
	VolumeSoft:    "speak softly",
	VolumeNormal:  "",
	VolumeLoud:    "speak loudly",
}

var paceTags = map[PaceIntent]string{
	PaceDefault: "",
	PaceSlow:    "speak slowly",
	PaceNormal:  "",
	PaceFast:    "speak quickly",
}

var cueTags = map[CueKind]string{
	CueShortPause: "pause",
	CueLongPause:  "long pause",
	CueLaugh:      "laugh",
	CueSigh:       "sigh",
	CueGasp:       "gasp",
	CueInhale:     "inhale",
	CueExhale:     "exhale",
}

type compiledPlan struct {
	text string
	plan ExpressionPlan
}

// Render validates a plan, strips untrusted bracket controls from its text,
// and renders the remaining plan using the fixed Fish Audio S2 tag whitelist.
func Render(plan ExpressionPlan) (Rendered, error) {
	compiled, err := compile(plan)
	if err != nil {
		return Rendered{}, err
	}

	insertions, err := makeInsertions(compiled)
	if err != nil {
		return Rendered{}, err
	}

	prefix := makePrefix(compiled.plan)
	return Rendered{
		PlainText:  compiled.text,
		FishS2Text: prefix + applyInsertions(compiled.text, insertions),
	}, nil
}

// Validate checks a plan without selecting a TTS provider. It validates every
// control directive as well as the sanitized text, so callers can reject an
// invalid LLM-produced plan before deciding whether to use Fish S2 or a plain
// text fallback.
func Validate(plan ExpressionPlan) error {
	compiled, err := compile(plan)
	if err != nil {
		return err
	}
	_, err = makeInsertions(compiled)
	return err
}

// PlainText validates and sanitizes a plan but intentionally ignores its
// expression controls. It is the safe fallback for TTS providers that do not
// support Fish S2-style tags.
func PlainText(plan ExpressionPlan) (string, error) {
	compiled, err := compile(plan)
	if err != nil {
		return "", err
	}
	if _, err := makeInsertions(compiled); err != nil {
		return "", err
	}
	return compiled.text, nil
}

// SanitizeText removes every square-bracketed sequence, including unknown S2
// natural-language cues, then normalizes whitespace. Removing arbitrary
// bracketed content is necessary because S2 accepts free-form bracket cues.
// An unmatched '[' or ']' is removed while its surrounding prose is retained.
func SanitizeText(input string) string {
	input = strings.ToValidUTF8(input, "")
	var out strings.Builder
	out.Grow(len(input))

	for index := 0; index < len(input); {
		if input[index] == '[' {
			if closeIndex := strings.IndexByte(input[index+1:], ']'); closeIndex >= 0 {
				index += closeIndex + 2
				continue
			}
			index++
			continue
		}
		if input[index] == ']' {
			index++
			continue
		}

		r, width := utf8.DecodeRuneInString(input[index:])
		index += width
		if unicode.IsControl(r) && !unicode.IsSpace(r) {
			continue
		}
		out.WriteRune(r)
	}
	return strings.Join(strings.Fields(out.String()), " ")
}

func compile(plan ExpressionPlan) (compiledPlan, error) {
	text := SanitizeText(plan.Text)
	if text == "" {
		return compiledPlan{}, ErrEmptyText
	}
	if utf8.RuneCountInString(text) > MaxTextRunes {
		return compiledPlan{}, ErrTextTooLong
	}
	if len(plan.Emphasis) > MaxEmphasisItems {
		return compiledPlan{}, ErrTooManyEmphasis
	}
	if len(plan.Cues) > MaxCueItems {
		return compiledPlan{}, ErrTooManyCues
	}
	if _, ok := emotionTags[plan.Emotion]; !ok {
		return compiledPlan{}, fmt.Errorf("unsupported emotion %q", plan.Emotion)
	}
	if _, ok := deliveryTags[plan.Delivery]; !ok {
		return compiledPlan{}, fmt.Errorf("unsupported delivery %q", plan.Delivery)
	}
	if _, ok := volumeTags[plan.Volume]; !ok {
		return compiledPlan{}, fmt.Errorf("unsupported volume intent %q", plan.Volume)
	}
	if _, ok := paceTags[plan.Pace]; !ok {
		return compiledPlan{}, fmt.Errorf("unsupported pace intent %q", plan.Pace)
	}

	return compiledPlan{text: text, plan: plan}, nil
}

type insertion struct {
	index    int
	priority int
	order    int
	value    string
}

func makeInsertions(compiled compiledPlan) ([]insertion, error) {
	insertions := make([]insertion, 0, len(compiled.plan.Emphasis)+len(compiled.plan.Cues))
	order := 0

	for _, emphasis := range compiled.plan.Emphasis {
		phrase, err := sanitizeAnchor(emphasis.Phrase, "emphasis phrase")
		if err != nil {
			return nil, err
		}
		if emphasis.Occurrence < 0 {
			return nil, fmt.Errorf("emphasis occurrence cannot be negative")
		}
		index, ok := occurrenceIndex(compiled.text, phrase, emphasis.Occurrence)
		if !ok {
			return nil, fmt.Errorf("emphasis phrase %q occurrence %d was not found", phrase, emphasis.Occurrence)
		}
		insertions = append(insertions, insertion{
			index:    index,
			priority: 1,
			order:    order,
			value:    "[emphasis] ",
		})
		order++
	}

	for _, cue := range compiled.plan.Cues {
		tag, ok := cueTags[cue.Kind]
		if !ok {
			return nil, fmt.Errorf("unsupported cue kind %q", cue.Kind)
		}
		if cue.Occurrence < 0 {
			return nil, fmt.Errorf("cue occurrence cannot be negative")
		}

		position := cue.Position
		switch position {
		case CueAfter, CueBefore:
			anchor, err := sanitizeAnchor(cue.Anchor, "cue anchor")
			if err != nil {
				return nil, err
			}
			index, ok := occurrenceIndex(compiled.text, anchor, cue.Occurrence)
			if !ok {
				return nil, fmt.Errorf("cue anchor %q occurrence %d was not found", anchor, cue.Occurrence)
			}
			if position == CueAfter {
				index += len(anchor)
				insertions = append(insertions, insertion{index: index, priority: 2, order: order, value: " [" + tag + "]"})
			} else {
				insertions = append(insertions, insertion{index: index, priority: 0, order: order, value: "[" + tag + "] "})
			}
		case CueStart:
			if strings.TrimSpace(cue.Anchor) != "" {
				return nil, fmt.Errorf("start cue cannot have an anchor")
			}
			insertions = append(insertions, insertion{index: 0, priority: 0, order: order, value: "[" + tag + "] "})
		case CueEnd:
			if strings.TrimSpace(cue.Anchor) != "" {
				return nil, fmt.Errorf("end cue cannot have an anchor")
			}
			insertions = append(insertions, insertion{index: len(compiled.text), priority: 2, order: order, value: " [" + tag + "]"})
		default:
			return nil, fmt.Errorf("unsupported cue position %q", cue.Position)
		}
		order++
	}

	return insertions, nil
}

func sanitizeAnchor(raw, field string) (string, error) {
	anchor := SanitizeText(raw)
	if anchor == "" {
		return "", fmt.Errorf("%s is required", field)
	}
	if utf8.RuneCountInString(anchor) > MaxAnchorRunes {
		return "", fmt.Errorf("%s exceeds maximum length", field)
	}
	return anchor, nil
}

func occurrenceIndex(text, phrase string, occurrence int) (int, bool) {
	start := 0
	for current := 0; current <= occurrence; current++ {
		offset := strings.Index(text[start:], phrase)
		if offset < 0 {
			return 0, false
		}
		index := start + offset
		if current == occurrence {
			return index, true
		}
		start = index + len(phrase)
	}
	return 0, false
}

func makePrefix(plan ExpressionPlan) string {
	tags := make([]string, 0, 4)
	for _, tag := range []string{
		emotionTags[plan.Emotion],
		deliveryTags[plan.Delivery],
		volumeTags[plan.Volume],
		paceTags[plan.Pace],
	} {
		if tag != "" {
			tags = append(tags, "["+tag+"]")
		}
	}
	if len(tags) == 0 {
		return ""
	}
	return strings.Join(tags, " ") + " "
}

func applyInsertions(text string, insertions []insertion) string {
	if len(insertions) == 0 {
		return text
	}
	sort.SliceStable(insertions, func(i, j int) bool {
		if insertions[i].index != insertions[j].index {
			return insertions[i].index < insertions[j].index
		}
		if insertions[i].priority != insertions[j].priority {
			return insertions[i].priority < insertions[j].priority
		}
		return insertions[i].order < insertions[j].order
	})

	var out strings.Builder
	out.Grow(len(text) + len(insertions)*12)
	previous := 0
	for _, item := range insertions {
		out.WriteString(text[previous:item.index])
		out.WriteString(item.value)
		previous = item.index
	}
	out.WriteString(text[previous:])
	return out.String()
}
