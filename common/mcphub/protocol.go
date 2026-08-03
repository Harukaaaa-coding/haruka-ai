package mcphub

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

type ProtocolClient interface {
	Initialize(context.Context) (ProtocolInfo, error)
	ListTools(context.Context) ([]mcp.Tool, error)
	CallTool(context.Context, string, map[string]any) (*mcp.CallToolResult, error)
	Close() error
}

type ProtocolClientFactory func(config ServerConfig, onToolsChanged func()) (ProtocolClient, error)

func DefaultProtocolClientFactory(lookup EnvLookup) ProtocolClientFactory {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	return func(config ServerConfig, onToolsChanged func()) (ProtocolClient, error) {
		options := []transport.StreamableHTTPCOption{transport.WithContinuousListening()}
		if config.Credential != nil {
			if err := credentialAvailable(config.Credential, lookup); err != nil {
				return nil, err
			}
			credential := *config.Credential
			options = append(options, transport.WithHTTPHeaderFunc(func(context.Context) map[string]string {
				value, ok := lookup(credential.Env)
				if !ok || value == "" || strings.ContainsAny(value, "\r\n") {
					return nil
				}
				if credential.Scheme != "" {
					value = credential.Scheme + " " + value
				}
				return map[string]string{credential.Header: value}
			}))
		}

		httpTransport, err := transport.NewStreamableHTTP(config.URL, options...)
		if err != nil {
			return nil, newError(ErrorInvalidConfig, "MCP server transport cannot be created", err)
		}
		upstream := client.NewClient(httpTransport)
		if onToolsChanged != nil {
			upstream.OnNotification(func(notification mcp.JSONRPCNotification) {
				if notification.Method == mcp.MethodNotificationToolsListChanged {
					onToolsChanged()
				}
			})
		}
		return &httpProtocolClient{client: upstream}, nil
	}
}

func credentialAvailable(reference *CredentialReference, lookup EnvLookup) error {
	value, ok := lookup(reference.Env)
	if !ok || value == "" {
		return newError(ErrorServerUnavailable, fmt.Sprintf("MCP credential environment variable %s is not configured", reference.Env), nil)
	}
	if strings.ContainsAny(value, "\r\n") {
		return newError(ErrorServerUnavailable, fmt.Sprintf("MCP credential environment variable %s is invalid", reference.Env), nil)
	}
	return nil
}

type httpProtocolClient struct {
	mu          sync.Mutex
	client      *client.Client
	initialized bool
	closed      bool
	info        ProtocolInfo
}

func (protocol *httpProtocolClient) Initialize(ctx context.Context) (ProtocolInfo, error) {
	protocol.mu.Lock()
	defer protocol.mu.Unlock()
	if protocol.closed {
		return ProtocolInfo{}, fmt.Errorf("MCP protocol client is closed")
	}
	if protocol.initialized {
		return protocol.info, nil
	}
	if err := protocol.client.Start(ctx); err != nil {
		return ProtocolInfo{}, fmt.Errorf("start MCP transport: %w", err)
	}
	request := mcp.InitializeRequest{}
	request.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	request.Params.ClientInfo = mcp.Implementation{Name: "GopherAI MCP Hub", Version: "1.0.0"}
	request.Params.Capabilities = mcp.ClientCapabilities{}
	initialized, err := protocol.client.Initialize(ctx, request)
	if err != nil {
		return ProtocolInfo{}, fmt.Errorf("initialize MCP protocol: %w", err)
	}
	protocol.info = ProtocolInfo{
		ProtocolVersion: initialized.ProtocolVersion,
		ServerName:      initialized.ServerInfo.Name,
		ServerVersion:   initialized.ServerInfo.Version,
	}
	protocol.initialized = true
	return protocol.info, nil
}

func (protocol *httpProtocolClient) ListTools(ctx context.Context) ([]mcp.Tool, error) {
	result, err := protocol.client.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("MCP tools/list: %w", err)
	}
	return append([]mcp.Tool(nil), result.Tools...), nil
}

func (protocol *httpProtocolClient) CallTool(ctx context.Context, name string, arguments map[string]any) (*mcp.CallToolResult, error) {
	request := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: arguments}}
	result, err := protocol.client.CallTool(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("MCP tools/call: %w", err)
	}
	return result, nil
}

func (protocol *httpProtocolClient) Close() error {
	protocol.mu.Lock()
	defer protocol.mu.Unlock()
	if protocol.closed {
		return nil
	}
	protocol.closed = true
	return protocol.client.Close()
}
