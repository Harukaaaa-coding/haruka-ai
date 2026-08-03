package speechgateway

import (
	"context"
	"strings"

	"GopherAI/common/asr"
)

// BaiduBatchProviderID identifies the existing non-streaming Baidu ASR
// implementation. Keeping the ID explicit lets a realtime profile select it
// as a safe fallback while new streaming implementations use their own IDs.
const BaiduBatchProviderID = "baidu-batch"

// ASRProvider is the provider-neutral contract for a complete audio segment.
// Streaming ASR will get a separate streaming contract later; it should not
// overload this batch API with partial-result lifecycle semantics.
type ASRProvider interface {
	ID() string
	Recognize(context.Context, asr.RecognitionRequest) (string, error)
}

// RegisterASR adds an ASR provider to the registry. Provider IDs are
// case-insensitive and are scoped independently from TTS provider IDs, so a
// profile can use a matching ID for input and output providers if desired.
func (r *Registry) RegisterASR(provider ASRProvider) error {
	if r == nil || provider == nil {
		return ErrProviderUnavailable
	}
	id := strings.ToLower(strings.TrimSpace(provider.ID()))
	if id == "" {
		return ErrProviderUnavailable
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.asrProviders == nil {
		r.asrProviders = make(map[string]ASRProvider)
	}
	if _, exists := r.asrProviders[id]; exists {
		return providerDuplicateError(id)
	}
	r.asrProviders[id] = provider
	return nil
}

// GetASR retrieves an ASR provider by its case-insensitive ID.
func (r *Registry) GetASR(id string) (ASRProvider, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	provider, ok := r.asrProviders[strings.ToLower(strings.TrimSpace(id))]
	return provider, ok
}

// BaiduASRProvider adapts the existing Baidu batch ASR service to the
// provider-neutral ASRProvider interface.
type BaiduASRProvider struct {
	service batchRecognizer
}

type batchRecognizer interface {
	Recognize(context.Context, asr.RecognitionRequest) (string, error)
}

// NewBaiduASRProvider wraps an initialized Baidu ASR service. Credentials and
// endpoint configuration remain owned by common/asr and are never copied into
// the provider registry.
func NewBaiduASRProvider(service *asr.Service) (*BaiduASRProvider, error) {
	if service == nil {
		return nil, ErrProviderUnavailable
	}
	return newBaiduASRProvider(service)
}

func newBaiduASRProvider(service batchRecognizer) (*BaiduASRProvider, error) {
	if service == nil {
		return nil, ErrProviderUnavailable
	}
	return &BaiduASRProvider{service: service}, nil
}

func (p *BaiduASRProvider) ID() string {
	if p == nil || p.service == nil {
		return ""
	}
	return BaiduBatchProviderID
}

func (p *BaiduASRProvider) Recognize(ctx context.Context, request asr.RecognitionRequest) (string, error) {
	if p == nil || p.service == nil {
		return "", ErrProviderUnavailable
	}
	return p.service.Recognize(ctx, request)
}
