package config

import (
	"errors"
	"flag"
	"fmt"
	"github.com/BurntSushi/toml"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
)

type Config struct {
	BaseURL           string
	Model             string
	APIKey            string
	MaxSteps          int
	AllowInsecureHTTP bool
}

type fileConfig struct {
	Model             *string `toml:"model"`
	BaseURL           *string `toml:"base_url"`
	MaxSteps          *int64  `toml:"max_steps"`
	AllowInsecureHTTP *bool   `toml:"allow_insecure_http"`
}

func Parse(args []string, stderr io.Writer) (Config, error) {
	var cfg Config
	var configPath, flagBaseURL, flagModel string
	var flagMaxSteps int
	var flagAllowInsecure bool
	flags := flag.NewFlagSet("host-scout", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&configPath, "config", "", "explicit TOML config file")
	flags.StringVar(&flagBaseURL, "base-url", "", "Responses API base URL")
	flags.StringVar(&flagModel, "model", "", "model name")
	flags.IntVar(&flagMaxSteps, "max-steps", 0, "maximum model calls per user turn; 0 means unlimited")
	flags.BoolVar(&flagAllowInsecure, "allow-insecure-http", false, "allow non-loopback HTTP model endpoint")
	if err := flags.Parse(args); err != nil {
		return Config{}, err
	}
	if flags.NArg() != 0 {
		return Config{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	provided := make(map[string]bool)
	flags.Visit(func(f *flag.Flag) { provided[f.Name] = true })
	cfg.BaseURL = "https://api.openai.com/v1"
	if strings.TrimSpace(configPath) != "" {
		var file fileConfig
		meta, err := toml.DecodeFile(configPath, &file)
		if err != nil {
			return Config{}, errors.New("cannot read or parse TOML config")
		}
		if len(meta.Undecoded()) > 0 {
			return Config{}, errors.New("unknown TOML config field")
		}
		if file.Model != nil {
			cfg.Model = *file.Model
		}
		if file.BaseURL != nil {
			cfg.BaseURL = *file.BaseURL
		}
		if file.MaxSteps != nil {
			if *file.MaxSteps < 0 || *file.MaxSteps > int64(^uint(0)>>1) {
				return Config{}, errors.New("max_steps must be a non-negative representable integer")
			}
			cfg.MaxSteps = int(*file.MaxSteps)
		}
		if file.AllowInsecureHTTP != nil {
			cfg.AllowInsecureHTTP = *file.AllowInsecureHTTP
		}
	}
	if value := os.Getenv("HOST_SCOUT_MODEL"); value != "" {
		cfg.Model = value
	}
	if value := os.Getenv("HOST_SCOUT_BASE_URL"); value != "" {
		cfg.BaseURL = value
	}
	if provided["model"] {
		cfg.Model = flagModel
	}
	if provided["base-url"] {
		cfg.BaseURL = flagBaseURL
	}
	if provided["max-steps"] {
		cfg.MaxSteps = flagMaxSteps
	}
	if provided["allow-insecure-http"] {
		cfg.AllowInsecureHTTP = flagAllowInsecure
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.APIKey = os.Getenv("HOST_SCOUT_API_KEY")
	if cfg.Model == "" {
		return Config{}, errors.New("model is required: set --model or HOST_SCOUT_MODEL")
	}
	parsed, err := url.Parse(cfg.BaseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return Config{}, errors.New("base URL must be an HTTP(S) URL without query or fragment")
	}
	if parsed.User != nil {
		return Config{}, errors.New("base URL must not contain credentials")
	}
	if parsed.Scheme == "http" && !isLoopback(parsed.Hostname()) && !cfg.AllowInsecureHTTP {
		return Config{}, errors.New("remote HTTP model endpoint requires --allow-insecure-http")
	}
	if cfg.MaxSteps < 0 {
		return Config{}, errors.New("max-steps must be non-negative")
	}
	return cfg, nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
