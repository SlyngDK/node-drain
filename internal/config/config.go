package config

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	kyaml "github.com/knadh/koanf/parsers/yaml"
	kenv "github.com/knadh/koanf/providers/env/v2"
	kfile "github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/rawbytes"
	"github.com/knadh/koanf/v2"
)

//go:embed default-config.yaml
var defaultConfigYaml []byte

var k = koanf.New(".")
var _config *Config

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
	ContainerNode bool `koanf:"containerNode"`
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

func LoadDefaultConfig() {
	err := k.Load(rawbytes.Provider(defaultConfigYaml), kyaml.Parser())
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to load default config: %v\n", err)
		os.Exit(1)
	}
}

func LoadConfig() (*Config, error) {

	err := k.Load(kenv.Provider(".", kenv.Opt{
		Prefix: "NODEDRAIN_",
		TransformFunc: func(k, v string) (string, any) {
			k = strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(k, "NODEDRAIN_")), "_", ".")
			if strings.Contains(v, " ") {
				return k, strings.Split(v, " ")
			}

			return k, v
		},
	}), nil)
	if err != nil {
		return nil, err
	}

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

	if err = loadConfigFiles(); err != nil {
		return nil, err
	}

	var conf Config
	err = k.Unmarshal("", &conf)
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
