package logs

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/config"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Log level constants
const (
	LogLevelTrace = "trace"
	LogLevelDebug = "debug"
	LogLevelInfo  = "info"
	LogLevelWarn  = "warn"
	LogLevelError = "error"
)

// DefaultLogConfig returns default logging configuration
func DefaultLogConfig() *config.LogConfig {
	return &config.LogConfig{
		Level:         LogLevelInfo,
		EnableFile:    false, // console by default
		EnableConsole: true,
		Filename:      "main.log",
		MaxSize:       10, // 10MB
		MaxBackups:    5,  // 5 backup files
		MaxAge:        30, // 30 days
		Compress:      true,
	}
}

// parseLevel maps a configured level name to a zap level. "trace" maps to
// debug, the most verbose zap level; unknown names fall back to info.
func parseLevel(name string) zapcore.Level {
	switch name {
	case LogLevelTrace, LogLevelDebug:
		return zap.DebugLevel
	case LogLevelWarn:
		return zap.WarnLevel
	case LogLevelError:
		return zap.ErrorLevel
	default:
		return zap.InfoLevel
	}
}

// SetupLogger creates a logger with file and console outputs based on configuration
func SetupLogger(config *config.LogConfig) (*zap.Logger, error) {
	if config == nil {
		config = DefaultLogConfig()
	}

	level := parseLevel(config.Level)

	var cores []zapcore.Core

	// Console output
	if config.EnableConsole {
		consoleEncoder := getJSONEncoder()
		consoleCore := zapcore.NewCore(
			consoleEncoder,
			zapcore.AddSync(os.Stderr),
			level,
		)
		cores = append(cores, consoleCore)
	}

	// File output
	if config.EnableFile {
		fileCore, err := createFileCore(config, level)
		if err != nil {
			return nil, fmt.Errorf("failed to create file core: %w", err)
		}
		cores = append(cores, fileCore)
	}

	if len(cores) == 0 {
		return nil, fmt.Errorf("no log outputs configured")
	}

	// Combine cores
	core := zapcore.NewTee(cores...)

	// Wrap with secret sanitizer for security
	sanitizedCore := NewSecretSanitizer(core)

	// Create logger with caller information
	logger := zap.New(sanitizedCore, zap.AddCaller(), zap.AddCallerSkip(1))

	return logger, nil
}

// createFileCore creates a file-based logging core
func createFileCore(config *config.LogConfig, level zapcore.Level) (zapcore.Core, error) {
	core, _, err := createFileCoreWithCloser(config, level)
	return core, err
}

// createFileCoreWithCloser is createFileCore plus the sink's closer, for the
// callers that own a logger's lifetime (per-server upstream loggers, which
// otherwise hold one file handle per server for the life of the process).
func createFileCoreWithCloser(config *config.LogConfig, level zapcore.Level) (zapcore.Core, io.Closer, error) {
	sink, err := newRotatingSink(config)
	if err != nil {
		return nil, nil, err
	}
	writeSyncer := zapcore.WriteSyncer(sink)

	encoder := getJSONEncoder()

	return zapcore.NewCore(
		encoder,
		writeSyncer,
		level,
	), sink, nil
}

// rotatingSink is the lumberjack-backed file sink behind every file logger.
// It is a WriteSyncer for zap and a Closer for whoever owns the logger's
// lifetime: lumberjack opens the file lazily on the first Write and reopens
// after Close, so closing an idle sink releases its handle at no cost to a
// later write.
type rotatingSink struct {
	*lumberjack.Logger
}

// Sync satisfies zapcore.WriteSyncer; lumberjack writes through to the OS
// file on every Write, so there is nothing buffered to flush.
func (s *rotatingSink) Sync() error { return nil }

func newRotatingSink(config *config.LogConfig) (*rotatingSink, error) {
	// Get log file path with custom directory support
	logFilePath, err := GetLogFilePathWithDir(config.LogDir, config.Filename)
	if err != nil {
		return nil, fmt.Errorf("failed to get log file path: %w", err)
	}

	// Create lumberjack logger for log rotation
	return &rotatingSink{Logger: &lumberjack.Logger{
		Filename:   logFilePath,
		MaxSize:    config.MaxSize,
		MaxBackups: config.MaxBackups,
		MaxAge:     config.MaxAge,
		Compress:   config.Compress,
	}}, nil
}

// getJSONEncoder returns a JSON encoder for structured logging
func getJSONEncoder() zapcore.Encoder {
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout(time.RFC3339Nano)
	encoderConfig.EncodeLevel = zapcore.LowercaseLevelEncoder
	encoderConfig.EncodeCaller = zapcore.ShortCallerEncoder
	return zapcore.NewJSONEncoder(encoderConfig)
}

// serverLogFilename builds the per-server log filename from a server name,
// sanitizing characters that would otherwise be interpreted as path separators.
// Official modelcontextprotocol/registry server names are namespace/name (e.g.
// "io.github.evidai/polymarket-guard"); without sanitizing the "/" the filename
// would resolve to a nested directory instead of a single flat log file.
// All derivation sites (file writers + tail reader) must call this
// so writes and reads agree on the same path.
func serverLogFilename(serverName string) string {
	return fmt.Sprintf("server-%s.log", sanitizeServerLogName(serverName))
}

// sanitizeServerLogName replaces every character that is not safe in a single
// filename element with "_", keeping ASCII letters, digits, ".", "-" and "_".
// The result is always a single path element (no "/" or "\\"), so it can never
// escape the log directory or create nested directories.
func sanitizeServerLogName(serverName string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '.', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, serverName)
}

func NewUpstreamServerLogger(config *config.LogConfig, serverName string) (*zap.Logger, io.Closer, error) {
	if config == nil {
		config = DefaultLogConfig()
	}

	// Create a copy of the config for the upstream server
	serverConfig := *config
	serverConfig.Filename = serverLogFilename(serverName)
	serverConfig.EnableConsole = false // Upstream servers only log to file

	level := parseLevel(serverConfig.Level)

	// Create file core for upstream server
	fileCore, closer, err := createFileCoreWithCloser(&serverConfig, level)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create file core for upstream server %s: %w", serverName, err)
	}

	// Wrap with secret sanitizer for security
	sanitizedCore := NewSecretSanitizer(fileCore)

	// Create logger with server name prefix
	logger := zap.New(sanitizedCore, zap.AddCaller(), zap.AddCallerSkip(1))
	logger = logger.With(zap.String("server", serverName))

	return logger, closer, nil
}

// CreateHTTPLogger creates a logger specifically for HTTP API requests
func CreateHTTPLogger(config *config.LogConfig) (*zap.Logger, error) {
	if config == nil {
		config = DefaultLogConfig()
	}

	// Create a copy of the config for HTTP logging
	httpConfig := *config
	httpConfig.Filename = "http.log"
	httpConfig.EnableConsole = false // HTTP logs only go to file
	httpConfig.EnableFile = true

	level := parseLevel(httpConfig.Level)

	// Create file core for HTTP logging
	fileCore, err := createFileCore(&httpConfig, level)
	if err != nil {
		return nil, fmt.Errorf("failed to create file core for HTTP logger: %w", err)
	}

	// Wrap with secret sanitizer for security
	sanitizedCore := NewSecretSanitizer(fileCore)

	// Create logger without caller info for cleaner HTTP logs
	logger := zap.New(sanitizedCore)
	logger = logger.With(zap.String("component", "http_api"))

	return logger, nil
}

// ReadUpstreamServerLogTail reads the last N lines from an upstream server log file
func ReadUpstreamServerLogTail(config *config.LogConfig, serverName string, lines int) ([]string, error) {
	if lines <= 0 {
		lines = 50
	}
	if lines > 500 {
		lines = 500
	}

	// A nil config falls through to GetLogFilePathWithDir's default log
	// directory.
	logDir := ""
	if config != nil {
		logDir = config.LogDir
	}
	filename := serverLogFilename(serverName)
	logFilePath, err := GetLogFilePathWithDir(logDir, filename)
	if err != nil {
		return nil, fmt.Errorf("failed to get log file path for server %s: %w", serverName, err)
	}

	return ReadTail(logFilePath, lines)
}
