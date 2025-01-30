package handler

import (
	"io"
	"log/slog"
	"path/filepath"

	"github.com/alexhokl/helper/iohelper"
	"github.com/gliderlabs/ssh"
	"github.com/pkg/sftp"
)

func GetFileSessionHandler(pathUsersDirectory string) func(ssh.Session) {
	return func(sess ssh.Session) {
		logger := slog.With(
			slog.String("user", sess.User()),
			slog.String("remote", sess.RemoteAddr().String()),
			slog.String("local", sess.LocalAddr().String()),
		)
		logger.Info("file session started")

		homePath := filepath.Clean(filepath.Join(pathUsersDirectory, sess.User()))
		if !iohelper.IsDirectoryExist(homePath) {
			err := iohelper.CreateDirectory(homePath)
			if err != nil {
				logger.Error(
					"unable to create user directory",
					slog.String("error", err.Error()),
				)
				return
			}
		}

		debugStream := io.Discard
		serverOptions := []sftp.ServerOption{
			sftp.WithDebug(debugStream),
			sftp.WithServerWorkingDirectory(homePath),
		}
		server, err := sftp.NewServer(
			sess,
			serverOptions...,
		)
		if err != nil {
			logger.Error("sftp server init error", slog.String("error", err.Error()))
			return
		}
		if err := server.Serve(); err == io.EOF {
			server.Close()
			logger.Info("sftp client exited session.")
		} else if err != nil {
			logger.Error("sftp server completed with error", slog.String("error", err.Error()))
		}
		logger.Info("file session completed")
	}
}
