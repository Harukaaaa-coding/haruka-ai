package aihelper

import (
	"context"
	"time"

	"GopherAI/common/modelgateway"
	"github.com/cloudwego/eino/schema"
)

// gatewayModel applies policies uniformly to all existing model adapters.  It
// also retains the caller's selector so switching between catalog IDs remains
// compatible with AIHelperManager's model identity check.
type gatewayModel struct {
	delegate AIModel
	selector string
	provider string
	timeout  time.Duration
}

func newGatewayModel(delegate AIModel, selector, provider string, timeout time.Duration) AIModel {
	if selector == "" {
		selector = delegate.GetModelType()
	}
	return &gatewayModel{delegate: delegate, selector: selector, provider: provider, timeout: timeout}
}

func (m *gatewayModel) GenerateResponse(ctx context.Context, messages []*schema.Message) (*schema.Message, error) {
	requestContext, cancel := modelgateway.WithTimeout(ctx, m.timeout)
	defer cancel()
	response, err := m.delegate.GenerateResponse(requestContext, messages)
	if err != nil {
		return nil, modelgateway.NormalizeError(m.provider, "generate", err)
	}
	return response, nil
}

func (m *gatewayModel) StreamResponse(ctx context.Context, messages []*schema.Message, callback StreamCallback) (string, error) {
	requestContext, cancel := modelgateway.WithTimeout(ctx, m.timeout)
	defer cancel()
	response, err := m.delegate.StreamResponse(requestContext, messages, callback)
	if err != nil {
		return "", modelgateway.NormalizeError(m.provider, "stream", err)
	}
	return response, nil
}

func (m *gatewayModel) GetModelType() string { return m.selector }
