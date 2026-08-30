package main

import (
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetConfiguration clears every viper key touched by these tests so each
// case starts from the unset state.
func resetConfiguration() {
	for _, key := range []string{
		"host_key", "ssh_port", "api_port", "path_users_directory",
		"administrative_users", "ssh_idle_timeout", "ssh_max_connections",
	} {
		viper.Set(key, nil)
	}
}

// configureValidSettings sets every required key to a valid value, leaving
// only the key under test to vary.
func configureValidSettings(t *testing.T) {
	t.Helper()

	viper.Set("host_key", "keys/ecdsa.key")
	viper.Set("ssh_port", 8822)
	viper.Set("api_port", 8880)
	viper.Set("path_users_directory", ".")
	viper.Set("administrative_users", []string{"alice"})
}

func TestGetConfiguration_DefaultTimeoutsAndConnections(t *testing.T) {
	resetConfiguration()
	configureValidSettings(t)

	config, err := getConfiguration()
	require.NoError(t, err)

	assert.Equal(t, 5*time.Minute, config.SSHIdleTimeout)
	assert.Equal(t, 100, config.SSHMaxConnections)
}

func TestGetConfiguration_CustomIdleTimeout(t *testing.T) {
	for _, raw := range []string{"90s", "5m", "1h", "1500ms"} {
		t.Run(raw, func(t *testing.T) {
			resetConfiguration()
			configureValidSettings(t)
			viper.Set("ssh_idle_timeout", raw)

			config, err := getConfiguration()
			require.NoError(t, err)
			assert.Equal(t, viper.GetDuration("ssh_idle_timeout"), config.SSHIdleTimeout)
		})
	}
}

func TestGetConfiguration_MalformedIdleTimeoutFails(t *testing.T) {
	// A malformed duration would make GetDuration return zero; the raw check
	// must turn that into a startup error rather than a silent disable.
	for _, raw := range []string{"5min", "later", "-5m", "0s"} {
		t.Run(raw, func(t *testing.T) {
			resetConfiguration()
			configureValidSettings(t)
			viper.Set("ssh_idle_timeout", raw)

			_, err := getConfiguration()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "ssh idle timeout")
		})
	}
}

func TestGetConfiguration_CustomMaxConnections(t *testing.T) {
	resetConfiguration()
	configureValidSettings(t)
	viper.Set("ssh_max_connections", 3)

	config, err := getConfiguration()
	require.NoError(t, err)
	assert.Equal(t, 3, config.SSHMaxConnections)
}

func TestGetConfiguration_ZeroMaxConnectionsMeansUnlimited(t *testing.T) {
	// Zero is a valid explicit choice meaning no cap, distinct from unset,
	// which keeps the default of 100.
	resetConfiguration()
	configureValidSettings(t)
	viper.Set("ssh_max_connections", 0)

	config, err := getConfiguration()
	require.NoError(t, err)
	assert.Equal(t, 0, config.SSHMaxConnections)
}

func TestGetConfiguration_NegativeMaxConnectionsFails(t *testing.T) {
	resetConfiguration()
	configureValidSettings(t)
	viper.Set("ssh_max_connections", -1)

	_, err := getConfiguration()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ssh max connections")
}