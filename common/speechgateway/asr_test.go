package speechgateway

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"GopherAI/common/asr"
)

func TestRegistryRegistersAndFindsASRProvider(t *testing.T) {
	registry := NewRegistry()
	provider := &fakeASRProvider{id: "Baidu-Batch", transcription: "hello"}

	if err := registry.RegisterASR(provider); err != nil {
		t.Fatalf("RegisterASR() error = %v", err)
	}
	got, ok := registry.GetASR(" baidu-batch ")
	if !ok || got != provider {
		t.Fatalf("GetASR() = %#v, %v; want registered provider, true", got, ok)
	}

	err := registry.RegisterASR(&fakeASRProvider{id: "BAIDU-BATCH"})
	if !errors.Is(err, ErrProviderDuplicate) {
		t.Fatalf("duplicate RegisterASR() error = %v, want ErrProviderDuplicate", err)
	}
	if _, ok := registry.GetASR("unknown"); ok {
		t.Fatal("GetASR() found an unregistered provider")
	}
}

func TestRegistryASRRejectsUnavailableProviders(t *testing.T) {
	registry := NewRegistry()
	if err := registry.RegisterASR(nil); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("RegisterASR(nil) error = %v, want ErrProviderUnavailable", err)
	}
	if err := registry.RegisterASR(&fakeASRProvider{}); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("RegisterASR(empty ID) error = %v, want ErrProviderUnavailable", err)
	}

	var nilRegistry *Registry
	if err := nilRegistry.RegisterASR(&fakeASRProvider{id: "test"}); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("nil Registry RegisterASR() error = %v, want ErrProviderUnavailable", err)
	}
	if _, ok := nilRegistry.GetASR("test"); ok {
		t.Fatal("nil Registry GetASR() unexpectedly found provider")
	}
}

func TestRegistryASRIsConcurrentSafe(t *testing.T) {
	registry := NewRegistry()
	const providerCount = 64

	errorsByWorker := make(chan error, providerCount)
	var group sync.WaitGroup
	for index := 0; index < providerCount; index++ {
		index := index
		group.Add(1)
		go func() {
			defer group.Done()
			id := fmt.Sprintf("provider-%d", index)
			provider := &fakeASRProvider{id: id}
			if err := registry.RegisterASR(provider); err != nil {
				errorsByWorker <- err
				return
			}
			if got, ok := registry.GetASR(id); !ok || got != provider {
				errorsByWorker <- fmt.Errorf("GetASR(%q) did not return registered provider", id)
			}
		}()
	}
	group.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		t.Error(err)
	}
}

func TestBaiduASRProviderDelegatesToBatchService(t *testing.T) {
	service := &fakeBatchRecognizer{transcription: " 转写结果 "}
	provider, err := newBaiduASRProvider(service)
	if err != nil {
		t.Fatalf("newBaiduASRProvider() error = %v", err)
	}
	if got, want := provider.ID(), BaiduBatchProviderID; got != want {
		t.Fatalf("ID() = %q, want %q", got, want)
	}

	request := asr.RecognitionRequest{
		Audio:      []byte{1, 2, 3},
		Format:     "pcm",
		SampleRate: 16000,
		Username:   "haruka",
	}
	transcription, err := provider.Recognize(context.Background(), request)
	if err != nil {
		t.Fatalf("Recognize() error = %v", err)
	}
	if got, want := transcription, " 转写结果 "; got != want {
		t.Errorf("transcription = %q, want %q", got, want)
	}
	if got, want := service.request, request; !sameRecognitionRequest(got, want) {
		t.Errorf("request = %#v, want %#v", got, want)
	}
}

func TestBaiduASRProviderRejectsNilService(t *testing.T) {
	if _, err := NewBaiduASRProvider(nil); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("NewBaiduASRProvider(nil) error = %v, want ErrProviderUnavailable", err)
	}

	var provider *BaiduASRProvider
	if _, err := provider.Recognize(context.Background(), asr.RecognitionRequest{}); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("nil provider Recognize() error = %v, want ErrProviderUnavailable", err)
	}
}

func TestBaiduASRProviderPropagatesServiceError(t *testing.T) {
	expected := errors.New("baidu unavailable")
	provider, err := newBaiduASRProvider(&fakeBatchRecognizer{err: expected})
	if err != nil {
		t.Fatalf("newBaiduASRProvider() error = %v", err)
	}
	if _, err := provider.Recognize(context.Background(), asr.RecognitionRequest{}); !errors.Is(err, expected) {
		t.Fatalf("Recognize() error = %v, want wrapped %v", err, expected)
	}
}

type fakeASRProvider struct {
	id            string
	transcription string
}

func (p *fakeASRProvider) ID() string { return p.id }

func (p *fakeASRProvider) Recognize(context.Context, asr.RecognitionRequest) (string, error) {
	return p.transcription, nil
}

type fakeBatchRecognizer struct {
	transcription string
	request       asr.RecognitionRequest
	err           error
}

func (f *fakeBatchRecognizer) Recognize(_ context.Context, request asr.RecognitionRequest) (string, error) {
	f.request = request
	return f.transcription, f.err
}

func sameRecognitionRequest(left, right asr.RecognitionRequest) bool {
	if left.Format != right.Format || left.SampleRate != right.SampleRate || left.Username != right.Username || len(left.Audio) != len(right.Audio) {
		return false
	}
	for index := range left.Audio {
		if left.Audio[index] != right.Audio[index] {
			return false
		}
	}
	return true
}
