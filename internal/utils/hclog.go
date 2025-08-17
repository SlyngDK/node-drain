package utils

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"runtime"
	"strings"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/slyngdk/node-drain/internal/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func Wrap(z *zap.Logger) hclog.Logger {
	z = z.WithOptions(zap.AddCallerSkip(1))
	return wrapper{zap: z}
}

type Level = hclog.Level

// Wrapper holds *zap.Logger and adapts its methods to declared by hclog.Logger.
type wrapper struct {
	zap *zap.Logger
}

func (w wrapper) Debug(msg string, args ...interface{}) {
	w.zap.Debug(msg, w.convertToZapAny(args...)...)
}
func (w wrapper) Info(msg string, args ...interface{}) {
	w.zap.Info(msg, w.convertToZapAny(args...)...)
}
func (w wrapper) Warn(msg string, args ...interface{}) {
	w.zap.Warn(msg, w.convertToZapAny(args...)...)
}
func (w wrapper) Error(msg string, args ...interface{}) {
	w.zap.Error(msg, w.convertToZapAny(args...)...)
}

// Log logs messages with four simplified levels - Debug,Warn,Error and Info as a default.
func (w wrapper) Log(lvl Level, msg string, args ...interface{}) {
	switch lvl {
	case hclog.Trace:
		w.Trace(msg, args...)
	case hclog.Debug:
		w.Debug(msg, args...)
	case hclog.Info:
		w.Info(msg, args...)
	case hclog.Warn:
		w.Warn(msg, args...)
	case hclog.Error:
		w.Error(msg, args...)
	case hclog.Off:
	default:
		w.Info(msg, args...)
	}
}

// Trace will log an info-level message in Zap.
func (w wrapper) Trace(msg string, args ...interface{}) {
	w.zap.Log(config.TraceLevel, msg, w.convertToZapAny(args...)...)
}

// With returns a logger with always-presented key-value pairs.
func (w wrapper) With(args ...interface{}) hclog.Logger {
	return &wrapper{zap: w.zap.With(w.convertToZapAny(args...)...)}
}

// Named returns a logger with the specific name.
// The name string will always be presented in a log messages.
func (w wrapper) Named(name string) hclog.Logger {
	return &wrapper{zap: w.zap.Named(name)}
}

// Name returns a logger's name (if presented).
func (w wrapper) Name() string { return w.zap.Name() }

// ResetNamed has the same implementation as Named.
func (w wrapper) ResetNamed(name string) hclog.Logger {
	return &wrapper{zap: w.zap.Named(name)}
}

// StandardWriter returns os.Stderr as io.Writer.
func (w wrapper) StandardWriter(opts *hclog.StandardLoggerOptions) io.Writer {
	return hclog.DefaultOutput
}

// StandardLogger returns standard logger with os.Stderr as a writer.
func (w wrapper) StandardLogger(opts *hclog.StandardLoggerOptions) *log.Logger {
	return log.New(w.StandardWriter(opts), "", log.LstdFlags)
}

func (w wrapper) IsTrace() bool {
	return w.zap.Level() <= config.TraceLevel
}

func (w wrapper) IsDebug() bool {
	return w.zap.Level() <= zapcore.DebugLevel
}

func (w wrapper) IsInfo() bool {
	return w.zap.Level() <= zapcore.InfoLevel
}

func (w wrapper) IsWarn() bool {
	return w.zap.Level() <= zapcore.WarnLevel
}

func (w wrapper) IsError() bool {
	return w.zap.Level() <= zapcore.ErrorLevel
}

// ImpliedArgs has no implementation.
func (w wrapper) ImpliedArgs() []interface{} { return nil }

// SetLevel has no implementation.
func (w wrapper) SetLevel(lvl Level) {}

func (w wrapper) GetLevel() hclog.Level {
	// Disable Plugin Stderr handling
	pc, _, _, ok := runtime.Caller(1)
	details := runtime.FuncForPC(pc)
	if ok && details != nil && details.Name() == "github.com/hashicorp/go-plugin.(*Client).logStderr" {
		return hclog.Off
	}

	if w.zap.Level() == config.TraceLevel {
		return hclog.Trace
	}
	return hclog.LevelFromString(w.zap.Level().String())
}

func (w wrapper) convertToZapAny(args ...interface{}) []zapcore.Field {
	fields := []zapcore.Field{}
	for i := len(args); i > 0; i -= 2 {
		left := i - 2
		if left < 0 {
			left = 0
		}

		items := args[left:i]

		switch l := len(items); l {
		case 2:
			k, ok := items[0].(string)
			if ok {
				fields = append(fields, zap.Any(k, items[1]))
			} else {
				fields = append(fields, zap.Any(fmt.Sprintf("arg%d", i-1), items[1]))
				fields = append(fields, zap.Any(fmt.Sprintf("arg%d", left), items[0]))
			}
		case 1:
			fields = append(fields, zap.Any(fmt.Sprintf("arg%d", left), items[0]))
		}
	}

	return fields
}

func getLevel(s string) zapcore.Level {
	switch s {
	case "trace", "off":
		return config.TraceLevel
	case "debug":
		return zapcore.DebugLevel
	case "warn":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}

func logJsonLog(l *zap.Logger, line string) error {
	var raw map[string]interface{}

	err := json.Unmarshal([]byte(line), &raw)
	if err != nil {
		return err
	}

	msg := ""
	if v, ok := raw["@message"]; ok {
		msg = v.(string)
		delete(raw, "@message")
	}

	level := zap.InfoLevel
	if v, ok := raw["@level"]; ok {
		levelS := v.(string)
		delete(raw, "@level")
		level = getLevel(levelS)
	}

	ce := l.Check(level, msg)
	if ce == nil {
		return nil
	}
	ce.Caller = zapcore.EntryCaller{}

	if v, ok := raw["@timestamp"]; ok {
		t, err := time.Parse("2006-01-02T15:04:05.000000Z07:00", v.(string))
		if err != nil {
			return err
		}
		ce.Time = t
		delete(raw, "@timestamp")
	}

	fields := make([]zapcore.Field, 0)

	for k, v := range raw {
		fields = append(fields, zap.Any(k, v))
	}

	ce.Write(fields...)
	return nil
}

func PluginOutputMonitor(l *zap.Logger) io.Writer {
	reader, writer := io.Pipe()

	go func() {
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 1024), 1024*1024) // 1MB max buffer
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if len(line) == 0 {
				continue
			}

			err := logJsonLog(l, line)
			if err != nil {
				switch line := line; {
				case strings.HasPrefix(line, "[TRACE]"):
					l.Log(config.TraceLevel, line)
				case strings.HasPrefix(line, "[DEBUG]"):
					l.Debug(line)
				case strings.HasPrefix(line, "[INFO]"):
					l.Info(line)
				case strings.HasPrefix(line, "[WARN]"):
					l.Warn(line)
				case strings.HasPrefix(line, "[ERROR]"):
					l.Error(line)
				case strings.HasPrefix(line, "panic: ") || strings.HasPrefix(line, "fatal error: "):
					l.Error(line)
				default:
					l.Info(line)
				}
			}
		}
	}()

	return writer
}
