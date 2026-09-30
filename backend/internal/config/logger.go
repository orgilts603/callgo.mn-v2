package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

func parseLogLevel(s string) (zerolog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return zerolog.InfoLevel, nil
	case "trace":
		return zerolog.TraceLevel, nil
	case "debug":
		return zerolog.DebugLevel, nil
	case "warn", "warning":
		return zerolog.WarnLevel, nil
	case "error":
		return zerolog.ErrorLevel, nil
	case "fatal":
		return zerolog.FatalLevel, nil
	case "panic":
		return zerolog.PanicLevel, nil
	}
	return zerolog.NoLevel, fmt.Errorf("unknown level %q (use trace, debug, info, warn, error)", s)
}

// NewLogger builds the process-wide zerolog logger: JSON on stderr in prod,
// human-readable console output when LogPretty is set.
func NewLogger(cfg Config) zerolog.Logger {
	level, err := parseLogLevel(cfg.LogLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}
	var w = os.Stderr
	var l zerolog.Logger
	if cfg.LogPretty {
		l = zerolog.New(zerolog.ConsoleWriter{Out: w, TimeFormat: time.TimeOnly})
	} else {
		l = zerolog.New(w)
	}
	return l.Level(level).With().Timestamp().Str("service", "callgo-backend").Logger()
}

// LogWarnings writes every load-time warning to the logger.
func (c Config) LogWarnings(log zerolog.Logger) {
	for _, w := range c.Warnings {
		log.Warn().Msg(w)
	}
}
