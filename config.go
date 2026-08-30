package main

import (
	"fmt"

	"github.com/alexhokl/helper/iohelper"
	"github.com/spf13/viper"
)

type FileServerConfiguration struct {
	HostKeyFile         string
	SSHServerPort       int
	APIServerPort       int
	PathUsersDirectory  string
	AdministrativeUsers []string
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

	config := &FileServerConfiguration{
		HostKeyFile:         pathHostKey,
		SSHServerPort:       serverPort,
		APIServerPort:       apiPort,
		PathUsersDirectory:  pathUsersDirectory,
		AdministrativeUsers: administrativeUsers,
	}

	return config, nil
}