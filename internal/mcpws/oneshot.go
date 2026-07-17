package mcpws

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"gitlab.flexinfer.ai/libs/mcp-go"
)

// JSON-RPC ids used for the one-shot call sequence.
const (
	oneshotInitializeID = 1
	oneshotCallToolID   = 2
)

// CallTool performs a one-shot MCP tools/call against a backend server:
// dial, initialize handshake, tools/call, close. It reuses the same
// backend-dial path as WebSocket sessions (registry URL first, host
// template fallback). Context cancellation closes the connection, which
// unblocks any pending read.
func (g *Gateway) CallTool(ctx context.Context, serverName, toolName string, arguments json.RawMessage) (json.RawMessage, error) {
	tr, err := g.dialBackendTransport(ctx, serverName, nil)
	if err != nil {
		return nil, fmt.Errorf("dial backend %q: %w", serverName, err)
	}
	defer func() { _ = tr.Close() }()

	// Close the transport when ctx is cancelled so blocked reads return.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = tr.Close()
		case <-done:
		}
	}()

	if err := oneshotInitialize(ctx, tr); err != nil {
		return nil, err
	}

	params := struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments,omitempty"`
	}{Name: toolName, Arguments: arguments}

	callReq, err := mcp.NewRequest(oneshotCallToolID, "tools/call", params)
	if err != nil {
		return nil, fmt.Errorf("build tools/call request: %w", err)
	}
	if err := tr.Send(ctx, callReq); err != nil {
		return nil, fmt.Errorf("send tools/call: %w", err)
	}

	resp, err := recvResponse(ctx, tr, oneshotCallToolID)
	if err != nil {
		return nil, fmt.Errorf("recv tools/call: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("backend error: %s", resp.Error.Message)
	}
	return resp.Result, nil
}

// oneshotInitialize runs the MCP initialize handshake on a fresh transport.
func oneshotInitialize(ctx context.Context, tr mcp.Transport) error {
	initReq, err := mcp.NewRequest(oneshotInitializeID, "initialize", mcp.InitializeParams{
		ProtocolVersion: mcp.ProtocolVersion,
		Capabilities:    mcp.Capabilities{},
		ClientInfo:      mcp.ClientInfo{Name: "fi-mcp-gateway-rest", Version: "1.0.0"},
	})
	if err != nil {
		return fmt.Errorf("build initialize request: %w", err)
	}
	if err := tr.Send(ctx, initReq); err != nil {
		return fmt.Errorf("send initialize: %w", err)
	}

	resp, err := recvResponse(ctx, tr, oneshotInitializeID)
	if err != nil {
		return fmt.Errorf("recv initialize: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("initialize error: %s", resp.Error.Message)
	}

	initialized := &mcp.Message{JSONRPC: mcp.JSONRPCVersion, Method: "notifications/initialized"}
	if err := tr.Send(ctx, initialized); err != nil {
		return fmt.Errorf("send initialized notification: %w", err)
	}
	return nil
}

// recvResponse reads messages until it sees the response matching wantID,
// skipping notifications and unrelated server-initiated messages.
func recvResponse(ctx context.Context, tr mcp.Transport, wantID int) (*mcp.Message, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		msg, err := tr.Recv(ctx)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, err
		}
		if msg.IsResponse() && idMatches(msg.ID, wantID) {
			return msg, nil
		}
	}
}

// idMatches compares a decoded JSON-RPC id against an expected integer id.
func idMatches(id any, want int) bool {
	switch v := id.(type) {
	case float64:
		return int(v) == want
	case int:
		return v == want
	case int64:
		return int(v) == want
	case json.Number:
		n, err := v.Int64()
		return err == nil && int(n) == want
	case string:
		return v == strconv.Itoa(want)
	default:
		return false
	}
}
