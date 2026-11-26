package gopentair

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/emergingrobotics/gopentair/internal/api"
	"github.com/emergingrobotics/gopentair/internal/auth"
)

// Region identifies the Cognito pools of a Pentair region.
type Region = auth.Region

// Built-in regions. RegionEU is experimental (client/identity IDs unknown).
var (
	RegionUS = api.RegionUS
	RegionEU = api.RegionEU
)

// RegionByName returns "us" or "eu".
func RegionByName(name string) (Region, error) {
	switch strings.ToLower(name) {
	case "", "us":
		return RegionUS, nil
	case "eu":
		return RegionEU, nil
	}
	return Region{}, fmt.Errorf("%w: unknown region %q (us|eu)", ErrInvalidValue, name)
}

// Duration is a time.Duration that unmarshals from "15s"-style strings.
type Duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// Config is the file/env configuration.
type Config struct {
	Username      string            `yaml:"username"`
	Password      string            `yaml:"password"`
	Region        string            `yaml:"region"`
	DefaultDevice string            `yaml:"default_device"`
	TokenFile     string            `yaml:"token_file"` // "none" disables caching
	Timeout       Duration          `yaml:"timeout"`
	RetryCount    int               `yaml:"retry_count"`
	PollInterval  Duration          `yaml:"poll_interval"`
	SignProfile   string            `yaml:"sign_profile"` // app | standard
	UserAgent     string            `yaml:"user_agent"`
	Debug         bool              `yaml:"debug"`
	Devices       map[string]string `yaml:"devices"` // alias -> device ID
}

// DefaultConfigPath returns ~/.config/pentair/config.yaml.
func DefaultConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "pentair", "config.yaml")
}

// LoadConfigFile reads a YAML config. A missing file yields an empty config
// and no error.
func LoadConfigFile(path string) (*Config, error) {
	if path == "" {
		path = DefaultConfigPath()
	}
	b, err := os.ReadFile(expandHome(path))
	if os.IsNotExist(err) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	c.TokenFile = expandHome(c.TokenFile)
	return &c, nil
}

// LoadConfigFromEnv reads PENTAIR_* environment variables.
func LoadConfigFromEnv() *Config {
	c := &Config{
		Username:      os.Getenv("PENTAIR_USERNAME"),
		Password:      os.Getenv("PENTAIR_PASSWORD"),
		Region:        os.Getenv("PENTAIR_REGION"),
		DefaultDevice: os.Getenv("PENTAIR_DEFAULT_DEVICE"),
		TokenFile:     expandHome(os.Getenv("PENTAIR_TOKEN_FILE")),
		SignProfile:   os.Getenv("PENTAIR_SIGN_PROFILE"),
	}
	if v, err := strconv.ParseBool(os.Getenv("PENTAIR_DEBUG")); err == nil {
		c.Debug = v
	}
	return c
}

// Merge copies non-zero fields of o into c.
func (c *Config) Merge(o *Config) {
	if o == nil {
		return
	}
	set := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	set(&c.Username, o.Username)
	set(&c.Password, o.Password)
	set(&c.Region, o.Region)
	set(&c.DefaultDevice, o.DefaultDevice)
	set(&c.TokenFile, o.TokenFile)
	set(&c.SignProfile, o.SignProfile)
	set(&c.UserAgent, o.UserAgent)
	if o.Timeout > 0 {
		c.Timeout = o.Timeout
	}
	if o.RetryCount > 0 {
		c.RetryCount = o.RetryCount
	}
	if o.PollInterval > 0 {
		c.PollInterval = o.PollInterval
	}
	if o.Debug {
		c.Debug = true
	}
	if len(o.Devices) > 0 {
		if c.Devices == nil {
			c.Devices = map[string]string{}
		}
		for k, v := range o.Devices {
			c.Devices[k] = v
		}
	}
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}
