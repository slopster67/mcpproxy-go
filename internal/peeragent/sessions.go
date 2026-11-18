package peeragent

import (
	"context"

	"github.com/mark3labs/mcp-go/v2/mcp"
	"github.com/slopster67/mcpproxy-go/internal/upstream/core"
	"github.com/slopster67/mcpproxy-go/internal/upstream/managed"
)

type peerSession struct {
	owner      *managed.Client
	connection *core.Client
}

func (a *Agent) getPeerSession(id string, owner *managed.Client) *core.Client {
	a.mu.Lock()
	defer a.mu.Unlock()
	session := a.sessions[id]
	if session.owner == owner {
		return session.connection
	}
	return nil
}

func (a *Agent) openPeerSession(ctx context.Context, id string, info mcp.Implementation, owner *managed.Client) (*mcp.InitializeResult, error) {
	if current := a.getPeerSession(id, owner); current != nil {
		return current.GetServerInfo(), nil
	}
	connection, err := owner.NewSessionConnection(id, info)
	if err != nil {
		return nil, err
	}
	if err := connection.Connect(ctx); err != nil {
		_ = connection.Disconnect()
		return nil, err
	}
	a.mu.Lock()
	a.sessions[id] = peerSession{owner, connection}
	a.mu.Unlock()
	return connection.GetServerInfo(), nil
}

func (a *Agent) closePeerSession(id string, owner *managed.Client) error {
	a.mu.Lock()
	session := a.sessions[id]
	if session.owner == owner {
		delete(a.sessions, id)
	}
	a.mu.Unlock()
	if session.owner == owner {
		return session.connection.Disconnect()
	}
	return nil
}

func (a *Agent) closePeerSessions(owner *managed.Client) {
	a.mu.Lock()
	var closing []*core.Client
	for id, session := range a.sessions {
		if owner == nil || owner == session.owner {
			delete(a.sessions, id)
			closing = append(closing, session.connection)
		}
	}
	a.mu.Unlock()
	for _, connection := range closing {
		_ = connection.Disconnect()
	}
}
