package handler

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/gliderlabs/ssh"
)

func HandleNormalSession(sess ssh.Session) {
	logger := slog.With(
		slog.String("user", sess.User()),
		slog.String("remote", sess.RemoteAddr().String()),
		slog.String("local", sess.LocalAddr().String()),
	)
	logger.Info("normal session")
	io.WriteString(
		sess,
		fmt.Sprintf(
			"Hi %s! You have successfully authenticated, but file server does not provide shell access.\n",
			sess.User(),
		),
	)
}

