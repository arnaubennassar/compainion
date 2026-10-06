package config

import "testing"

func TestValidateDefaultLoopback(t *testing.T) {
	t.Setenv("COMPANIOND_ALLOW_NON_LOOPBACK", "")
	c := Config{Addr: "127.0.0.1:7777"}
	if err := c.Validate(); err != nil {
		t.Fatalf("default loopback address should validate, got %v", err)
	}
}

func TestValidateNonLoopbackRejected(t *testing.T) {
	t.Setenv("COMPANIOND_ALLOW_NON_LOOPBACK", "")
	c := Config{Addr: "0.0.0.0:7777"}
	if err := c.Validate(); err == nil {
		t.Fatal("0.0.0.0 should be rejected")
	}
}

func TestValidateNonLoopbackAllowedWithEnv(t *testing.T) {
	t.Setenv("COMPANIOND_ALLOW_NON_LOOPBACK", "1")
	c := Config{Addr: "0.0.0.0:7777"}
	if err := c.Validate(); err != nil {
		t.Fatalf("non-loopback should be allowed with env var, got %v", err)
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv("COMPANIOND_ADDR", "")
	t.Setenv("COMPANIOND_DB", "")
	t.Setenv("XDG_DATA_HOME", "")
	c := Load()
	if c.Addr != "127.0.0.1:7777" {
		t.Fatalf("Addr = %q, want default 127.0.0.1:7777", c.Addr)
	}
	if c.DBPath == "" {
		t.Fatal("DBPath should default to ~/.local/share/companion/companion.db")
	}
}
