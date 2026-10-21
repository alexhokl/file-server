package main

import (
	"fmt"
	"time"

	"github.com/alexhokl/helper/iohelper"
	"github.com/spf13/viper"
)

const defaultSSHIdleTimeout = 5 * time.Minute
const defaultSSHMaxConnections = 100

type FileServerConfiguration struct {
	HostKeyFile         string
	SSHServerPort       int
	APIServerPort       int
	PathUsersDirectory  string
	AdministrativeUsers []string
	SSHIdleTimeout      time.Duration
	SSHMaxConnections   int
}

func getConfiguration() (*FileServerConfiguration, error) {
	pathHostKey := viper.GetString("host_key")
	if !iohelper.IsFileExist(pathHostKey) {
		return nil, fmt.Errorf("host key file does not exist: %s", pathHostKey)
	}
	serverPort := viper.GetInt("ssh_port")
	if serverPort <= 0 {
		return nil, fmt.Errorf("ssh server port is invalid: %d", serverPort)
	}
	apiPort := viper.GetInt("api_port")
	if apiPort <= 0 {
		return nil, fmt.Errorf("API server port is invalid: %d", apiPort)
	}
	pathUsersDirectory := viper.GetString("path_users_directory")
	if !iohelper.IsDirectoryExist(pathUsersDirectory) {
		return nil, fmt.Errorf("path users directory does not exist: %s", pathUsersDirectory)
	}
	administrativeUsers := viper.GetStringSlice("administrative_users")
	if len(administrativeUsers) == 0 {
		return nil, fmt.Errorf("administrative users are not set")
	}
	// The raw value is inspected first because a malformed duration makes
	// GetDuration return zero: without this check a typo would silently
	// disable the idle timeout instead of failing startup.
	sshIdleTimeout := defaultSSHIdleTimeout
	if raw := viper.GetString("ssh_idle_timeout"); raw != "" {
		sshIdleTimeout = viper.GetDuration("ssh_idle_timeout")
		if sshIdleTimeout <= 0 {
			return nil, fmt.Errorf("ssh idle timeout is invalid: %q", raw)
		}
	}
	sshMaxConnections := defaultSSHMaxConnections
	if viper.IsSet("ssh_max_connections") {
		sshMaxConnections = viper.GetInt("ssh_max_connections")
		if sshMaxConnections < 0 {
			return nil, fmt.Errorf("ssh max connections is invalid: %d", sshMaxConnections)
		}
	}

	config := &FileServerConfiguration{
		HostKeyFile:         pathHostKey,
		SSHServerPort:       serverPort,
		APIServerPort:       apiPort,
		PathUsersDirectory:  pathUsersDirectory,
		AdministrativeUsers: administrativeUsers,
		SSHIdleTimeout:      sshIdleTimeout,
		SSHMaxConnections:   sshMaxConnections,
	}

	return config, nil
}
