package config

import (
	"log/slog"
	"testing"
)

func TestConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		addr    string
		level   slog.Level
		wantErr bool
	}{
		{name: "defaults", addr: "127.0.0.1:5722", level: slog.LevelInfo},
		{name: "override", env: map[string]string{
			"PIXIV_CURATOR_ADDR": "0.0.0.0:8080", "PIXIV_CURATOR_LOG_LEVEL": " DEBUG ",
		}, addr: "0.0.0.0:8080", level: slog.LevelDebug},
		{name: "ipv6", env: map[string]string{"PIXIV_CURATOR_ADDR": "[::1]:5722"},
			addr: "[::1]:5722", level: slog.LevelInfo},
		{name: "missing port", env: map[string]string{"PIXIV_CURATOR_ADDR": "localhost"}, wantErr: true},
		{name: "invalid port", env: map[string]string{"PIXIV_CURATOR_ADDR": "localhost:70000"}, wantErr: true},
		{name: "zero port", env: map[string]string{"PIXIV_CURATOR_ADDR": "localhost:0"}, wantErr: true},
		{name: "named port", env: map[string]string{"PIXIV_CURATOR_ADDR": "localhost:http"}, wantErr: true},
		{name: "invalid log level", env: map[string]string{"PIXIV_CURATOR_LOG_LEVEL": "verbose"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := load(func(key string) string { return tt.env[key] })
			if (err != nil) != tt.wantErr {
				t.Fatalf("load error = %v; want error = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if cfg.Addr != tt.addr || cfg.LogLevel != tt.level {
				t.Fatalf("configuration = %+v; want addr %q, level %v", cfg, tt.addr, tt.level)
			}
		})
	}
}
