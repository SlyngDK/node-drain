package config

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/knadh/koanf/providers/rawbytes"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	kyaml "github.com/knadh/koanf/parsers/yaml"
	kfile "github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

//go:embed default-config.yaml
var defaultConfigYaml []byte

var k = koanf.New(".")
var _config *Config
var _loggerConfig *zap.Config

type Logger struct {
	Level string `koanf:"level"`
}

type Config struct {
	Log struct {
		Level   string            `koanf:"level"`
		Format  string            `koanf:"format"`
		Loggers map[string]Logger `koanf:"loggers"`
	} `koanf:"log"`
	Reboot struct {
		CheckInterval time.Duration `koanf:"checkInterval"`
	}
}

func (c *Config) GetLogger(name string) Logger {
	if logger, ok := c.Log.Loggers[name]; ok {
		if logger.Level == "" {
			logger.Level = c.Log.Level
		}
		return logger
	}
	return Logger{
		Level: c.Log.Level,
	}
}

func GetNamedLogger(name string) (*zap.Logger, error) {
	logger := GetConfig().GetLogger(name)

	level, err := zap.ParseAtomicLevel(logger.Level)
	if err != nil {
		zap.S().With(zap.Error(err)).Warn("failed to parse log level for named logger, using info", zap.String("name", name))
		level = zap.NewAtomicLevelAt(zapcore.InfoLevel)
	}
	_loggerConfig.Level = level
	l, err := _loggerConfig.Build()
	if err != nil {
		return nil, fmt.Errorf("error building named zap logger for %s: %w", name, err)
	}
	return l.Named(name), nil
}

func SetLoggerConfig(loggerConfig *zap.Config) {
	_loggerConfig = loggerConfig
}

func LoadDefaultConfig() {
	err := k.Load(rawbytes.Provider(defaultConfigYaml), kyaml.Parser())
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to load default config: %v\n", err)
		os.Exit(1)
	}
}

func LoadConfig() (*Config, error) {
	loadConfigFiles := func() error {
		configDir := "/config"
		stat, err := os.Stat(configDir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("failed to get stat for /config: %w", err)
		}
		if !stat.IsDir() {
			return fmt.Errorf("%s is not a directory", stat.Name())
		}

		files, err := os.ReadDir(configDir)
		if err != nil {
			return fmt.Errorf("failed to read config directory: %w", err)
		}

		sort.Slice(files, func(i, j int) bool {
			return files[i].Name() < files[j].Name()
		})

		for _, f := range files {
			ext := filepath.Ext(f.Name())
			switch ext {
			case ".yaml", ".yml":
				err := k.Load(kfile.Provider(filepath.Join(configDir, f.Name())), kyaml.Parser())
				if err != nil {
					_, _ = fmt.Fprintf(os.Stderr, "failed to load config from file %s: %v\n", f.Name(), err)
				}
			}
		}
		return nil
	}

	if err := loadConfigFiles(); err != nil {
		return nil, err
	}

	var conf Config
	err := k.Unmarshal("", &conf)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	_config = &conf
	return &conf, nil
}

func GetKoanf() *koanf.Koanf {
	return k
}

func GetConfig() *Config {
	return _config
}
