package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	gossh "golang.org/x/crypto/ssh"
	"gorm.io/gorm"

	"github.com/alexhokl/file-server/api"
	"github.com/alexhokl/file-server/db"
	"github.com/alexhokl/file-server/handler"
	"github.com/alexhokl/helper/cli"
	"github.com/alexhokl/helper/database"
	"github.com/gliderlabs/ssh"
	"github.com/spf13/viper"
)

const SHUTDOWN_TIMEOUT_IN_SECONDS = 10
const HTTP_SERVER_READ_HEADER_TIMEOUT_IN_SECONDS = 5
const databaseMaxOpenConnections = 20
const databaseMaxIdleConnections = 20
const databaseConnectionMaxIdleTime = 30 * time.Minute

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	// applies logger to both slog and log
	slog.SetDefault(logger)

	cli.ConfigureViper("", "file-server", false, "fileserver")

	pathDatabaseConnectionString := viper.GetString("path_database_connection_string")
	if pathDatabaseConnectionString == "" {
		slog.Error("database connection string is not set")
		os.Exit(1)
	}

	dialector, err := database.GetDatabaseDailector(pathDatabaseConnectionString)
	if err != nil {
		slog.Error(
			"unable to get database dailector",
			slog.String("error", err.Error()),
		)
		os.Exit(1)
	}

	dbConn, err := gorm.Open(dialector, &gorm.Config{TranslateError: true})
	if err != nil {
		slog.Error(
			"unable to connect to database",
			slog.String("error", err.Error()),
		)
		os.Exit(1)
	}

	slog.Info("database connection established")
	sqlDB, err := dbConn.DB()
	if err != nil {
		slog.Error(
			"unable to get database connection pool",
			slog.String("error", err.Error()),
		)
		os.Exit(1)
	}
	sqlDB.SetMaxOpenConns(databaseMaxOpenConnections)
	sqlDB.SetMaxIdleConns(databaseMaxIdleConnections)
	sqlDB.SetConnMaxIdleTime(databaseConnectionMaxIdleTime)
	err = db.Migrate(dbConn)
	if err != nil {
		slog.Error(
			"unable to migrate database",
			slog.String("error", err.Error()),
		)
		os.Exit(1)
	}
	slog.Info("database migration completed")

	config, err := getConfiguration()
	if err != nil {
		slog.Error(
			"unable to get configuration",
			slog.String("error", err.Error()),
		)
		os.Exit(1)
	}

	privateKeyBytes, err := os.ReadFile(config.HostKeyFile)
	if err != nil {
		slog.Error(
			"unable to read host key file",
			slog.String("error", err.Error()),
			slog.String("file", config.HostKeyFile),
		)
		os.Exit(1)
	}
	hostkey, err := gossh.ParsePrivateKey(privateKeyBytes)
	if err != nil {
		if _, ok := err.(*gossh.PassphraseMissingError); ok {
			slog.Error(
				"unable to parse host key with encrpytion",
				slog.String("error", err.Error()),
				slog.String("file", config.HostKeyFile),
			)
			os.Exit(1)
		}

		slog.Error(
			"unable to parse host key",
			slog.String("error", err.Error()),
			slog.String("file", config.HostKeyFile),
		)
		os.Exit(1)
	}

	sftpHandler, err := handler.GetFileSystemRequestHandler(config.PathUsersDirectory)
	if err != nil {
		slog.Error(
			"unable to create SFTP handler",
			slog.String("error", err.Error()),
		)
		os.Exit(1)
	}

	server := ssh.Server{
		Addr:    fmt.Sprintf(":%d", config.SSHServerPort),
		Handler: handler.HandleNormalSession,
		SubsystemHandlers: map[string]ssh.SubsystemHandler{
			"sftp": sftpHandler,
		},
		PublicKeyHandler: getPublicKeyHandler(dbConn),
		HostSigners:      []ssh.Signer{hostkey},
		// Cut connections that go silent, while active transfers keep
		// resetting the deadline. MaxTimeout is deliberately unset so long
		// transfers and persistent mounts are not arbitrarily killed.
		IdleTimeout: config.SSHIdleTimeout,
	}

	sshListener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		slog.Error(
			"unable to listen on SSH port",
			slog.String("error", err.Error()),
			slog.String("addr", server.Addr),
		)
		os.Exit(1)
	}
	cappedListener := newLimitedListener(sshListener, config.SSHMaxConnections)

	go func() {
		slog.Info("starting server", slog.String("addr", server.Addr))
		if err := server.Serve(cappedListener); err != nil && err != ssh.ErrServerClosed {
			slog.Error(
				"unable to start server",
				slog.String("error", err.Error()),
			)
		} else {
			slog.Info("server stopped")
		}
	}()

	apiRouter := api.GetRouter(dbConn)

	httpServer := &http.Server{
		Addr:              fmt.Sprintf(":%d", config.APIServerPort),
		Handler:           apiRouter,
		ReadHeaderTimeout: HTTP_SERVER_READ_HEADER_TIMEOUT_IN_SECONDS * time.Second,
	}

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("unable to start API server", slog.String("error", err.Error()))
		}
	}()

	<-ctx.Done()

	stop()
	slog.Info("shutting down gracefully, press Ctrl+C again to force")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), SHUTDOWN_TIMEOUT_IN_SECONDS*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error(
			"server forced to shutdown",
			slog.String("error", err.Error()),
		)
	}
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error(
			"API server forced to shutdown",
			slog.String("error", err.Error()),
		)
	}

	slog.Info("Server exiting")
}

func getPublicKeyHandler(dbConn *gorm.DB) ssh.PublicKeyHandler {
	return func(ctx ssh.Context, key ssh.PublicKey) bool {
		var credentials []db.UserCredential
		tx := dbConn.Where("username = ?", ctx.User()).Find(&credentials)
		if tx.Error != nil {
			slog.Error(
				"unable to get user credential",
				slog.String("error", tx.Error.Error()),
				slog.String("user", ctx.User()),
			)
			return false
		}
		if len(credentials) == 0 {
			slog.Info(
				"no credentials found",
				slog.String("user", ctx.User()),
			)
			return false
		}
		for _, c := range credentials {
			expectedKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(c.PublicKey))
			if err != nil {
				slog.Error(
					"unable to parse public key",
					slog.String("error", err.Error()),
					slog.String("user", ctx.User()),
					slog.String("remote", ctx.RemoteAddr().String()),
					slog.String("local", ctx.LocalAddr().String()),
					slog.String("key", c.PublicKey),
				)
				continue
			}
			if ssh.KeysEqual(key, expectedKey) {
				return true
			}
		}
		return false
	}
}
