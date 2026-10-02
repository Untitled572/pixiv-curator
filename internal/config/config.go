package config

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Addr     string
	LogLevel slog.Level
}

func Load() (Config, error) {
	return load(os.Getenv)
}

func load(getenv func(string) string) (Config, error) {
	cfg := Config{Addr: "127.0.0.1:5722", LogLevel: slog.LevelInfo}
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
