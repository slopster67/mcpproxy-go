package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/v2/mcp"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"

	"github.com/slopster67/mcpproxy-go/internal/auth"
	"github.com/slopster67/mcpproxy-go/internal/mcprelay"
)

type continuation struct {
	owner    [32]byte
	ctx      context.Context
	cancel   context.CancelFunc
	active   sync.Mutex
	mu       sync.Mutex
	notify   func(mcp.JSONRPCNotification)
	progress float64
	events   chan continuationEvent
	answers  map[string]chan mcp.InputResponse
	requests mcp.InputRequests
}

type continuationEvent struct {
	result *mcp.CallToolResult
	err    error
}

func (p *MCPProxyServer) continueTool(next mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		caller := []any{principalFromContext(ctx)}
		if ac := auth.AuthContextFromContext(ctx); ac != nil {
			caller = append(caller, ac.ClientID, ac.HostID, ac.AllowManaging, ac.AllowCodeExecution)
		}
		identity, err := json.Marshal([]any{request.Params.Name, request.GetArguments(), caller, p.cacheAuthorization(ctx), relayCapabilities(ctx)})
		if err != nil {
			return nil, err
		}
		owner := sha256.Sum256(identity)
		token := request.Params.RequestState
		p.continuationMu.Lock()
		state := p.continuations[token]
		if token == "" {
			if len(p.continuations) >= 256 {
				p.continuationMu.Unlock()
				return nil, fmt.Errorf("too many pending tool calls")
			}
			executionCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
			state = &continuation{owner: owner, ctx: executionCtx, cancel: cancel, events: make(chan continuationEvent, 1), answers: make(map[string]chan mcp.InputResponse), requests: make(mcp.InputRequests)}
			token = uuid.NewString()
			if p.continuations == nil {
				p.continuations = make(map[string]*continuation)
			}
			p.continuations[token] = state
		} else if state == nil || state.owner != owner || state.ctx.Err() != nil {
			p.continuationMu.Unlock()
			return nil, fmt.Errorf("invalid or expired tool continuation")
		}
		p.continuationMu.Unlock()
		if !state.active.TryLock() {
			return nil, fmt.Errorf("tool continuation is already in use")
		}
		defer state.active.Unlock()
		state.mu.Lock()
		state.notify = func(notification mcp.JSONRPCNotification) {
			if notification.Method == string(mcp.MethodNotificationProgress) {
				if request.Params.Meta == nil || request.Params.Meta.ProgressToken == nil {
					return
				}
				state.progress++
				notification.Params.AdditionalFields["progressToken"] = request.Params.Meta.ProgressToken
				notification.Params.AdditionalFields["progress"] = state.progress
				delete(notification.Params.AdditionalFields, "total")
			}
			if srv := mcpserver.ServerFromContext(ctx); srv != nil {
				_ = srv.SendNotificationToClient(ctx, notification.Method, notification.Params.AdditionalFields)
			}
		}
		state.mu.Unlock()
		defer func() { state.mu.Lock(); state.notify = nil; state.mu.Unlock() }()

		if request.Params.RequestState == "" {
			capabilities := relayCapabilities(ctx)
			relay := &mcprelay.Request{Meta: request.Params.Meta, Capabilities: capabilities}
			relay.Notify = func(notification mcp.JSONRPCNotification) {
				state.mu.Lock()
				defer state.mu.Unlock()
				if state.notify != nil {
					state.notify(notification)
				}
			}
			relay.Input = func(inputCtx context.Context, input mcp.InputRequest) (mcp.InputResponse, error) {
				switch input.Method {
				case mcp.MethodElicitationCreate:
					if capabilities.Elicitation == nil {
						return mcp.InputResponse{}, fmt.Errorf("client does not support elicitation")
					}
				case mcp.MethodSamplingCreateMessage:
					if capabilities.Sampling == nil {
						return mcp.InputResponse{}, fmt.Errorf("client does not support sampling")
					}
				case mcp.MethodListRoots:
					if capabilities.Roots == nil {
						return mcp.InputResponse{}, fmt.Errorf("client does not support roots")
					}
				default:
					return mcp.InputResponse{}, fmt.Errorf("unsupported input method %s", input.Method)
				}
				id := uuid.NewString()
				answerCh := make(chan mcp.InputResponse, 1)
				state.mu.Lock()
				state.answers[id] = answerCh
				state.requests[id] = input
				state.mu.Unlock()
				result := &mcp.CallToolResult{Result: mcp.Result{ResultType: mcp.ResultTypeInputRequired}, MultiRoundTripResult: mcp.MultiRoundTripResult{InputRequests: mcp.InputRequests{id: input}, RequestState: token}}
				select {
				case state.events <- continuationEvent{result: result}:
				case <-inputCtx.Done():
					return mcp.InputResponse{}, inputCtx.Err()
				}
				select {
				case answer := <-answerCh:
					return answer, nil
				case <-inputCtx.Done():
					return mcp.InputResponse{}, inputCtx.Err()
				}
			}
			go func() {
				var result *mcp.CallToolResult
				var err error
				func() {
					defer func() {
						if recovered := recover(); recovered != nil {
							err = fmt.Errorf("tool handler panicked: %v", recovered)
						}
					}()
					result, err = next(mcprelay.WithRequest(state.ctx, relay), request)
				}()
				select {
				case state.events <- continuationEvent{result: result, err: err}:
				case <-state.ctx.Done():
				}
			}()
			go func() {
				<-state.ctx.Done()
				p.continuationMu.Lock()
				delete(p.continuations, token)
				p.continuationMu.Unlock()
			}()
		} else {
			state.mu.Lock()
			for id, response := range request.Params.InputResponses {
				if ch := state.answers[id]; ch != nil {
					ch <- response
					delete(state.answers, id)
					delete(state.requests, id)
				}
			}
			if len(state.requests) > 0 {
				result := state.pending(token)
				state.mu.Unlock()
				return result, nil
			}
			state.mu.Unlock()
		}
		for {
			select {
			case event := <-state.events:
				if event.result != nil && event.result.NeedsInput() {
					state.mu.Lock()
					if len(state.requests) == 0 {
						state.mu.Unlock()
						continue
					}
					event.result = state.pending(token)
					state.mu.Unlock()
				} else {
					state.cancel()
				}
				return event.result, event.err
			case <-state.ctx.Done():
				return nil, state.ctx.Err()
			case <-ctx.Done():
				state.cancel()
				return nil, ctx.Err()
			}
		}
	}
}

func (state *continuation) pending(token string) *mcp.CallToolResult {
	requests := make(mcp.InputRequests, len(state.requests))
	for id, request := range state.requests {
		requests[id] = request
	}
	return &mcp.CallToolResult{Result: mcp.Result{ResultType: mcp.ResultTypeInputRequired}, MultiRoundTripResult: mcp.MultiRoundTripResult{InputRequests: requests, RequestState: token}}
}

func relayCapabilities(ctx context.Context) mcp.ClientCapabilities {
	capabilities := mcp.ClientCapabilities{}
	if info := mcpserver.RequestProtocolInfoFromContext(ctx); info != nil && info.ClientCapabilities != nil {
		capabilities.Elicitation = info.ClientCapabilities.Elicitation
		capabilities.Sampling = info.ClientCapabilities.Sampling
		capabilities.Roots = info.ClientCapabilities.Roots
	}
	return capabilities
}
