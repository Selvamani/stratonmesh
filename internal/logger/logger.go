package logger

import (
	"os"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New creates a structured logger. Pass "development" for verbose, "production" for JSON.
func New(mode string) *zap.SugaredLogger {
	var cfg zap.Config
	if mode == "development" {
		cfg = zap.NewDevelopmentConfig()
		cfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	} else {
		cfg = zap.NewProductionConfig()
	}

	if lvl := os.Getenv("SM_LOG_LEVEL"); lvl != "" {
		var level zapcore.Level
		if err := level.UnmarshalText([]byte(lvl)); err == nil {
			cfg.Level.SetLevel(level)
		}
	}

	l, err := cfg.Build()
	if err != nil {
		panic("failed to create logger: " + err.Error())
	}
	return l.Sugar()
}
