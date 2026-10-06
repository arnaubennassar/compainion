package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Config holds the daemon settings.
type Config struct {
	Addr   string
	DBPath string
}

// Load reads configuration from the environment:
//   - COMPANIOND_ADDR: listen address (default 127.0.0.1:7777)
//   - COMPANIOND_DB: SQLite path (default $XDG_DATA_HOME/companion/companion.db,
//     falling back to ~/.local/share/companion/companion.db)
func Load() Config {
	return Config{
		Addr:   envOr("COMPANIOND_ADDR", "127.0.0.1:7777"),
		DBPath: envOr("COMPANIOND_DB", defaultDBPath()),
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func defaultDBPath() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "companion", "companion.db")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "companion.db"
	}
	return filepath.Join(home, ".local", "share", "companion", "companion.db")
}

// Validate returns an error if the host of Addr is not loopback, unless
// COMPANIOND_ALLOW_NON_LOOPBACK=1.
func (c Config) Validate() error {
	if os.Getenv("COMPANIOND_ALLOW_NON_LOOPBACK") == "1" {
		return nil
	}
	host, _, err := splitHostPort(c.Addr)
	if err != nil {
		return err
	}
	if !isLoopback(host) {
		return errors.New("non-loopback bind address " + c.Addr + " refused (set COMPANIOND_ALLOW_NON_LOOPBACK=1 to allow)")
	}
	return nil
}

func splitHostPort(addr string) (host, port string, err error) {
	last := strings.LastIndex(addr, ":")
	if last < 0 {
		return "", "", errors.New("invalid address (missing port): " + addr)
	}
	host, port = addr[:last], addr[last+1:]
	if host == "" {
		return "", "", errors.New("invalid address (missing host): " + addr)
	}
	return host, port, nil
}

func isLoopback(host string) bool {
	return host == "127.0.0.1" || host == "::1" || strings.EqualFold(host, "localhost")
}
