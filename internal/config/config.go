package config

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config holds all application settings loaded from config.toml
type Config struct {
	Server   ServerConfig   `toml:"server"`
	Database DatabaseConfig `toml:"database"`
	User     UserConfig     `toml:"user"`
	Cache    CacheConfig    `toml:"cache"`
	Images   ImagesConfig   `toml:"images"`
}

type ServerConfig struct {
	// Listen is the full address to listen on, e.g. "0.0.0.0:8080".
	// If set, it takes precedence over Host and Port.
	Listen string `toml:"listen"`
	Host   string `toml:"host"`
	Port   int    `toml:"port"`
}

type DatabaseConfig struct {
	// Path to the decrypted QQ SQLite database file
	Path string `toml:"path"`
}

type UserConfig struct {
	// Your own QQ number; messages from this UIN will be shown on the right side
	MyQQ uint64 `toml:"my_qq"`
	// If true, show your own messages on the right side (like WeChat/QQ style)
	BubbleOnRight bool `toml:"bubble_on_right"`
}

type CacheConfig struct {
	// Directory to store cached avatars and nicknames
	Dir string `toml:"dir"`
	// Maximum number of avatar requests per second
	AvatarRateLimit float64 `toml:"avatar_rate_limit"`
}

type ImagesConfig struct {
	// Parent directory for user images referenced in chat (e.g. path containing Image71\...)
	BaseDir string `toml:"base_dir"`
}

// DefaultConfig returns a Config with sensible defaults
func DefaultConfig() *Config {
	home, _ := os.UserHomeDir()
	return &Config{
		Server: ServerConfig{
			Host: "127.0.0.1",
			Port: 8080,
		},
		Database: DatabaseConfig{
			Path: "nt_msg.db",
		},
		User: UserConfig{
			MyQQ:          0,
			BubbleOnRight: true,
		},
		Cache: CacheConfig{
			Dir:             filepath.Join(home, "qqviewer_cache"),
			AvatarRateLimit: 2.0,
		},
		Images: ImagesConfig{
			BaseDir: "",
		},
	}
}

// Load reads the config from the given path; if the file doesn't exist, returns defaults.
func Load(path string) (*Config, error) {
	cfg := DefaultConfig()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return cfg, nil
	}
	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Save writes the config to the given path (creates parent dirs if needed).
func Save(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(cfg)
}
