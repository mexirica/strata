package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mexirica/strata/internal/cid"
	"github.com/mexirica/strata/internal/node"
	"github.com/spf13/viper"
	"go.yaml.in/yaml/v3"
)

type config struct {
	DataDir         string `mapstructure:"data_dir" yaml:"data_dir"`
	HashAlgorithm   string `mapstructure:"hash_algorithm" yaml:"hash_algorithm"`
	MinChunkSize    int    `mapstructure:"min_chunk_size" yaml:"min_chunk_size"`
	NormalChunkSize int    `mapstructure:"normal_chunk_size" yaml:"normal_chunk_size"`
	MaxChunkSize    int    `mapstructure:"max_chunk_size" yaml:"max_chunk_size"`
	MaxFileSize     int64  `mapstructure:"max_file_size" yaml:"max_file_size"`
	MaxChunks       int    `mapstructure:"max_chunks" yaml:"max_chunks"`
	MaxNameBytes    int    `mapstructure:"max_name_bytes" yaml:"max_name_bytes"`
}

func defaultConfig() config {
	return config{
		DataDir:         ".strata/data",
		HashAlgorithm:   "blake3",
		MinChunkSize:    256 * 1024,
		NormalChunkSize: 1024 * 1024,
		MaxChunkSize:    4 * 1024 * 1024,
		MaxFileSize:     10 * 1024 * 1024 * 1024,
		MaxChunks:       40960,
		MaxNameBytes:    4096,
	}
}

func loadConfig(path string) (config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetEnvPrefix("STRATA")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	defaults := defaultConfig()
	v.SetDefault("data_dir", defaults.DataDir)
	v.SetDefault("hash_algorithm", defaults.HashAlgorithm)
	v.SetDefault("min_chunk_size", defaults.MinChunkSize)
	v.SetDefault("normal_chunk_size", defaults.NormalChunkSize)
	v.SetDefault("max_chunk_size", defaults.MaxChunkSize)
	v.SetDefault("max_file_size", defaults.MaxFileSize)
	v.SetDefault("max_chunks", defaults.MaxChunks)
	v.SetDefault("max_name_bytes", defaults.MaxNameBytes)
	if err := v.ReadInConfig(); err != nil {
		return config{}, fmt.Errorf("read config %q: %w", path, err)
	}
	var cfg config
	if err := v.Unmarshal(&cfg); err != nil {
		return config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if !filepath.IsAbs(cfg.DataDir) {
		cfg.DataDir = filepath.Join(filepath.Dir(path), cfg.DataDir)
	}
	return cfg, nil
}

func (c config) nodeConfig() (node.Config, error) {
	var algorithm cid.Algorithm
	switch strings.ToLower(c.HashAlgorithm) {
	case "blake3":
		algorithm = cid.AlgBlake3
	case "sha256":
		algorithm = cid.AlgSHA256
	default:
		return node.Config{}, fmt.Errorf("unsupported hash_algorithm %q", c.HashAlgorithm)
	}
	return node.Config{
		DataDir:         c.DataDir,
		HashAlgorithm:   algorithm,
		MinChunkSize:    c.MinChunkSize,
		NormalChunkSize: c.NormalChunkSize,
		MaxChunkSize:    c.MaxChunkSize,
		MaxFileSize:     c.MaxFileSize,
		MaxChunks:       c.MaxChunks,
		MaxNameBytes:    c.MaxNameBytes,
	}, nil
}

func writeDefaultConfig(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("config file %q already exists", path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect config file: %w", err)
	}
	data, err := yaml.Marshal(defaultConfig())
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}
