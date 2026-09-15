package config

import (
	"testing"
)

func TestCLIValidate(t *testing.T) {
	t.Run("fails when metrics protected and password empty", func(t *testing.T) {
		var cli CLI
		cli.Metrics.Port = "2112"
		cli.Metrics.Protected = true
		cli.Metrics.Password = ""
		if err := cli.Validate(); err == nil {
			t.Error("expected validation error for empty metrics password, got nil")
		}
	})

	t.Run("passes when metrics protected and password set", func(t *testing.T) {
		var cli CLI
		cli.Metrics.Port = "2112"
		cli.Metrics.Protected = true
		cli.Metrics.Password = "secret123"
		if err := cli.Validate(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("passes when metrics port is 0 or disabled", func(t *testing.T) {
		var cli CLI
		cli.Metrics.Port = "0"
		cli.Metrics.Protected = true
		cli.Metrics.Password = ""
		if err := cli.Validate(); err != nil {
			t.Errorf("unexpected error when port is 0: %v", err)
		}
	})
}
