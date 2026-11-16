package mcprelay

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/v2/client/transport"
	"github.com/mark3labs/mcp-go/v2/mcp"
)

type key struct{}
type subscriptionKey struct{}

func WithSubscription(ctx context.Context, observe func(mcp.RequestId)) context.Context {
	return context.WithValue(ctx, subscriptionKey{}, observe)
}

type Request struct {
	Meta         *mcp.Meta
	Capabilities mcp.ClientCapabilities
	Input        func(context.Context, mcp.InputRequest) (mcp.InputResponse, error)
	Notify       func(mcp.JSONRPCNotification)
}

func WithRequest(ctx context.Context, request *Request) context.Context {
	return context.WithValue(ctx, key{}, request)
}
func FromContext(ctx context.Context) *Request {
	request, _ := ctx.Value(key{}).(*Request)
	return request
}

func Prepare(ctx context.Context, request transport.JSONRPCRequest) (transport.JSONRPCRequest, error) {
	if request.Method == string(mcp.MethodSubscriptionsListen) {
		if observe, ok := ctx.Value(subscriptionKey{}).(func(mcp.RequestId)); ok {
			observe(request.ID)
		}
	}
	if request.Method != string(mcp.MethodToolsCall) {
		return request, nil
	}
	relay := FromContext(ctx)
	if relay == nil {
		return request, nil
	}
	raw, err := json.Marshal(request.Params)
	if err != nil {
		return request, err
	}
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		return request, err
	}
	meta, _ := params["_meta"].(map[string]any)
	if meta == nil {
		meta = make(map[string]any)
	}
	meta[mcp.MetaKeyClientCapabilities] = relay.Capabilities
	params["_meta"] = meta
	request.Params = params
	return request, nil
}

type Handler struct{}

func input(ctx context.Context, request mcp.InputRequest) (mcp.InputResponse, error) {
	relay := FromContext(ctx)
	if relay == nil || relay.Input == nil {
		return mcp.InputResponse{}, fmt.Errorf("client input is unavailable")
	}
	return relay.Input(ctx, request)
}

func (Handler) Elicit(ctx context.Context, request mcp.ElicitationRequest) (*mcp.ElicitationResult, error) {
	response, err := input(ctx, mcp.NewElicitationInputRequest(request.Params))
	if err != nil {
		return nil, err
	}
	if err := response.DecodeFor(mcp.MethodElicitationCreate); err != nil {
		return nil, err
	}
	return response.Elicitation, nil
}
func (Handler) CreateMessage(ctx context.Context, request mcp.CreateMessageRequest) (*mcp.CreateMessageResult, error) {
	response, err := input(ctx, mcp.NewSamplingInputRequest(request.CreateMessageParams))
	if err != nil {
		return nil, err
	}
	if err := response.DecodeFor(mcp.MethodSamplingCreateMessage); err != nil {
		return nil, err
	}
	return response.Sampling, nil
}
func (Handler) ListRoots(ctx context.Context, request mcp.ListRootsRequest) (*mcp.ListRootsResult, error) {
	response, err := input(ctx, mcp.NewRootsInputRequest())
	if err != nil {
		return nil, err
	}
	if err := response.DecodeFor(mcp.MethodListRoots); err != nil {
		return nil, err
	}
	return response.Roots, nil
}
