# file-server

Secure file server with the following characteristics.

- Public key cryptography is used for authentication
- User information is stored in a PostgreSQL database
- No shell file access

:warning: This is a work in progress and not ready for production yet :warning:

database tables
- users
- user_keys

## Configuration

Configuration is loaded via [viper](https://github.com/spf13/viper). Environment
variables take precedence over the configuration file.

### Command-line flags

The server accepts no command-line flags or arguments.

### Configuration file

A configuration file named `.file-server.<ext>` in the user's home directory is
loaded automatically if it exists. Supported extensions include `yaml`, `yml`,
`json`, `toml`, `properties`, and `ini`. Keys are the environment variables
without the `FILESERVER_` prefix.

```yaml
host_key: ./keys/ecdsa.key
ssh_port: 8822
api_port: 8880
path_users_directory: ./data/files
path_database_connection_string: ./keys/database_connection_string
administrative_users:
  - alex
```

### Environment variables

All environment variables are prefixed with `FILESERVER_`.

| Variable | Description |
|---|---|
| `FILESERVER_PATH_DATABASE_CONNECTION_STRING` | File path to a text file containing the PostgreSQL connection string on its first line |
| `FILESERVER_HOST_KEY` | File path to the SSH host private key |
| `FILESERVER_SSH_PORT` | Port of the SSH/SFTP server |
| `FILESERVER_API_PORT` | Port of the HTTP API server |
| `FILESERVER_PATH_USERS_DIRECTORY` | Directory path for data storage, must exist |
| `FILESERVER_ADMINISTRATIVE_USERS` | Space-separated list of administrative usernames, must not be empty |

`task run` sets all of the above with development defaults before starting the server.

## References

- [SSH File Transfer
  Protocol](https://filezilla-project.org/specs/draft-ietf-secsh-filexfer-02.txt#section-6.3)
- [Request based SFTP API - pkg/sftp](https://github.com/pkg/sftp/blob/master/request-readme.md)
