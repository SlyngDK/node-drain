package config

import (
	"fmt"
	"strings"
	"sync"

	"github.com/fatih/color"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var _loggerConfig *zap.Config
var _loggerSync sync.Mutex

const TraceLevel = zapcore.DebugLevel - 1

func GetLogger(logLevel, logFormat string) (*zap.Logger, error) {
	level, err := zap.ParseAtomicLevel(logLevel)
	if err != nil {
		return nil, fmt.Errorf("failed to parse log level: %w", err)
	}

	disableStackTrace := true
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.EncodeLevel = func(l zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
		if logFormat == "console" {
			if l == TraceLevel {
				enc.AppendString(color.CyanString("TRACE"))
				return
			}
			zapcore.CapitalColorLevelEncoder(l, enc)
			return
		}
		if l == TraceLevel {
			enc.AppendString("trace")
			return
		}
		zapcore.LowercaseLevelEncoder(l, enc)
	}

	if logFormat == "json" {
		disableStackTrace = false
	}
	if logFormat == "console" {
		encoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	}

	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	loggerConfig := zap.Config{
		Level:             level,
		Encoding:          logFormat,
		EncoderConfig:     encoderConfig,
		OutputPaths:       []string{"stdout"},
		ErrorOutputPaths:  []string{"stderr"},
		DisableStacktrace: disableStackTrace,
	}

	logger, err := loggerConfig.Build()

	if err != nil {
		return nil, fmt.Errorf("failed to build logger: %w", err)
	}
	setLoggerConfig(&loggerConfig)
	return logger, nil
}

func GetNamedLogger(name string) (*zap.Logger, error) {
	_loggerSync.Lock()
	defer _loggerSync.Unlock()
	logger := GetConfig().GetLogger(name)

	var level zap.AtomicLevel
	var err error

	if strings.ToLower(logger.Level) == "trace" {
		level = zap.NewAtomicLevelAt(TraceLevel)
	} else {
		level, err = zap.ParseAtomicLevel(logger.Level)
		if err != nil {
			zap.S().With(zap.Error(err)).Warn("failed to parse log level for named logger, using info", zap.String("name", name))
			level = zap.NewAtomicLevelAt(zapcore.InfoLevel)
		}
	}

	_loggerConfig.Level = level
	l, err := _loggerConfig.Build()
	if err != nil {
		return nil, fmt.Errorf("error building named zap logger for %s: %w", name, err)
	}
	return l.Named(name), nil
}

func setLoggerConfig(loggerConfig *zap.Config) {
	_loggerSync.Lock()
	defer _loggerSync.Unlock()
	_loggerConfig = loggerConfig
}
