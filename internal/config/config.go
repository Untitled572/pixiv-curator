package config

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Addr            string
	LogLevel        slog.Level
	DatabasePath    string
	AdminTokenFile  string
	PublicBaseURL   string
	PixivTokenFile  string
	OAuthConfigFile string
	LoginPython     string
	LoginHelper     string
}

func Load() (Config, error) {
	return load(os.Getenv)
}

func load(getenv func(string) string) (Config, error) {
	cfg := Config{Addr: "127.0.0.1:5722", LogLevel: slog.LevelInfo}
	cfg.DatabasePath = strings.TrimSpace(getenv("PIXIV_CURATOR_DATABASE_PATH"))
	if cfg.DatabasePath == "" {
		cfg.DatabasePath = "data/curator.db"
	}
	cfg.AdminTokenFile = strings.TrimSpace(getenv("PIXIV_CURATOR_ADMIN_TOKEN_FILE"))
	cfg.PixivTokenFile = strings.TrimSpace(getenv("PIXIV_CURATOR_PIXIV_TOKEN_FILE"))
	cfg.OAuthConfigFile = strings.TrimSpace(getenv("PIXIV_CURATOR_OAUTH_CONFIG_FILE"))
	cfg.LoginPython = strings.TrimSpace(getenv("PIXIV_CURATOR_LOGIN_PYTHON"))
	cfg.LoginHelper = strings.TrimSpace(getenv("PIXIV_CURATOR_LOGIN_HELPER"))
	if (cfg.LoginPython == "") != (cfg.LoginHelper == "") {
		return Config{}, fmt.Errorf("PIXIV_CURATOR_LOGIN_PYTHON and PIXIV_CURATOR_LOGIN_HELPER must be configured together")
	}
	if cfg.LoginPython != "" && (cfg.PixivTokenFile == "" || cfg.OAuthConfigFile == "") {
		return Config{}, fmt.Errorf("browser login requires Pixiv token and OAuth configuration paths")
	}
	cfg.PublicBaseURL = strings.TrimRight(strings.TrimSpace(getenv("PIXIV_CURATOR_PUBLIC_BASE_URL")), "/")
	if cfg.PublicBaseURL == "" {
		cfg.PublicBaseURL = "http://127.0.0.1:5722"
	}
	base, err := url.Parse(cfg.PublicBaseURL)
	if err != nil || base.Hostname() == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Path != "" {
		return Config{}, fmt.Errorf("PIXIV_CURATOR_PUBLIC_BASE_URL must be an absolute HTTP(S) origin")
	}
	if addr := strings.TrimSpace(getenv("PIXIV_CURATOR_ADDR")); addr != "" {
		cfg.Addr = addr
	}
	_, port, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return Config{}, fmt.Errorf("PIXIV_CURATOR_ADDR must be host:port: %w", err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return Config{}, fmt.Errorf("PIXIV_CURATOR_ADDR port must be between 1 and 65535")
	}

	switch level := strings.ToLower(strings.TrimSpace(getenv("PIXIV_CURATOR_LOG_LEVEL"))); level {
	case "", "info":
		cfg.LogLevel = slog.LevelInfo
	case "debug":
		cfg.LogLevel = slog.LevelDebug
	case "warn":
		cfg.LogLevel = slog.LevelWarn
	case "error":
		cfg.LogLevel = slog.LevelError
	default:
		return Config{}, fmt.Errorf("PIXIV_CURATOR_LOG_LEVEL must be debug, info, warn or error")
	}
	return cfg, nil
}
