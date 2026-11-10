package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/contracts"
	"github.com/slopster67/mcpproxy-go/internal/logs"
)

func (s *Server) GetServerLogs(name string, tail int) ([]contracts.LogEntry, error) {
	if tail <= 0 {
		tail = 100
	}
	if tail > 500 {
		tail = 500
	}
	var lines []string
	if host := s.runtime.ServerHost(name); host != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := s.runtime.Peers().Call(ctx, host, "logs", name, map[string]int{"tail": tail}, &lines); err != nil {
			return nil, err
		}
	} else {
		cfg, err := s.runtime.GetConfig()
		if err != nil {
			return nil, err
		}
		found := false
		for _, server := range cfg.Servers {
			if server != nil && server.Name == name {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("%w: %s", contracts.ErrServerNotFound, name)
		}
		lines, err = logs.ReadUpstreamServerLogTail(cfg.EffectiveLogConfig(), name, tail)
		if err != nil {
			return nil, err
		}
	}
	entries := make([]contracts.LogEntry, 0, len(lines))
	for _, line := range lines {
		if entry, ok := parseLogLine(line, name); ok {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

// Only the JSON record emitted by MCPProxy's logger is accepted. Old formats,
// torn writes and malformed records are skipped, never assigned a read-time date.
func parseLogLine(line, name string) (contracts.LogEntry, bool) {
	var fields map[string]interface{}
	if json.Unmarshal([]byte(line), &fields) != nil {
		return contracts.LogEntry{}, false
	}
	timestamp, _ := fields["ts"].(string)
	message, hasMessage := fields["msg"].(string)
	level, hasLevel := fields["level"].(string)
	stamp, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil || !hasMessage || !hasLevel {
		return contracts.LogEntry{}, false
	}
	for _, key := range []string{"ts", "msg", "level", "server"} {
		delete(fields, key)
	}
	fields = liveRedaction.Value("", fields).(map[string]interface{})
	return contracts.LogEntry{Timestamp: stamp, Message: scrubUpstreamText(message), Level: strings.ToUpper(level), Server: name, Fields: fields}, true
}
