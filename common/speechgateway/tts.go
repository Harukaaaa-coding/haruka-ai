// Package speechgateway provides provider-neutral speech output contracts.
// It is intentionally separate from common/modelgateway: end-to-end voice
// providers and independently switchable TTS providers have different
// capability and credential lifecycles from text LLMs.
package speechgateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"GopherAI/common/fishaudio"
	"GopherAI/common/voicecontrol"
)

var (
	ErrProviderUnavailable = errors.New("speech provider is unavailable")
	ErrProviderDuplicate   = errors.New("speech provider is already registered")
)

// Audio is one complete, bounded audio segment.  Realtime transports may
// forward its bytes directly after emitting a metadata event.
type Audio struct {
	Bytes       []byte
	ContentType string
	Format      string
	ProviderID  string
}

type TTSProvider interface {
	ID() string
	Synthesize(context.Context, voicecontrol.ExpressionPlan) (Audio, error)
}

type Registry struct {
	mu           sync.RWMutex
	providers    map[string]TTSProvider
	asrProviders map[string]ASRProvider
}

func NewRegistry() *Registry {
	return &Registry{
		providers:    make(map[string]TTSProvider),
		asrProviders: make(map[string]ASRProvider),
	}
}

func (r *Registry) Register(provider TTSProvider) error {
	if r == nil || provider == nil {
		return ErrProviderUnavailable
	}
	id := strings.ToLower(strings.TrimSpace(provider.ID()))
	if id == "" {
		return ErrProviderUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = make(map[string]TTSProvider)
	}
	if _, exists := r.providers[id]; exists {
		return providerDuplicateError(id)
	}
	r.providers[id] = provider
	return nil
}

func providerDuplicateError(id string) error {
	return fmt.Errorf("%w: %s", ErrProviderDuplicate, id)
}

func (r *Registry) Get(id string) (TTSProvider, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	provider, ok := r.providers[strings.ToLower(strings.TrimSpace(id))]
	return provider, ok
}

// FishS2Provider owns rendering of the generic expression plan.  Raw S2
// markup therefore never crosses a browser boundary or becomes a user-settable
// provider parameter.
type FishS2Provider struct {
	client *fishaudio.Client
	id     string
}

func NewFishS2Provider(id string, client *fishaudio.Client) (*FishS2Provider, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" || client == nil {
		return nil, ErrProviderUnavailable
	}
	return &FishS2Provider{id: id, client: client}, nil
}

func (p *FishS2Provider) ID() string {
	if p == nil {
		return ""
	}
	return p.id
}

func (p *FishS2Provider) Synthesize(ctx context.Context, plan voicecontrol.ExpressionPlan) (Audio, error) {
	if p == nil || p.client == nil {
		return Audio{}, ErrProviderUnavailable
	}
	rendered, err := voicecontrol.Render(plan)
	if err != nil {
		return Audio{}, err
	}
	options := fishaudio.DefaultOptions()
	options.Format = "mp3"
	options.Latency = "low"
	applyFishProsody(&options, plan)
	response, err := p.client.Synthesize(ctx, fishaudio.Request{
		Text:    rendered.FishS2Text,
		Options: options,
	})
	if err != nil {
		return Audio{}, err
	}
	contentType := strings.TrimSpace(response.ContentType)
	if contentType == "" {
		contentType = contentTypeForFormat(response.Format)
	}
	return Audio{
		Bytes:       response.Audio,
		ContentType: contentType,
		Format:      response.Format,
		ProviderID:  p.id,
	}, nil
}

func applyFishProsody(options *fishaudio.Options, plan voicecontrol.ExpressionPlan) {
	if options == nil {
		return
	}
	switch plan.Pace {
	case voicecontrol.PaceSlow:
		options.Speed = fishaudio.Float64(0.9)
	case voicecontrol.PaceFast:
		options.Speed = fishaudio.Float64(1.12)
	}
	switch plan.Volume {
	case voicecontrol.VolumeSoft:
		options.Volume = fishaudio.Float64(-3)
	case voicecontrol.VolumeLoud:
		options.Volume = fishaudio.Float64(3)
	}
}

func contentTypeForFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "wav":
		return "audio/wav"
	case "pcm":
		return "audio/pcm"
	case "opus":
		return "audio/ogg; codecs=opus"
	default:
		return "audio/mpeg"
	}
}
