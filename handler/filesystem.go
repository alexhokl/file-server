package handler

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"

	"github.com/alexhokl/helper/iohelper"
	"github.com/gliderlabs/ssh"
	"github.com/pkg/sftp"
)

const defaultFileWritePermission = 0600
const defaultFileReadPermission = 0400

func GetFileSystemRequestHandler(pathUsersDirectory string) func(ssh.Session) {
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

		requestHandler := getFileSystemRequestHandler(sess.User(), homePath)

		serverOptions := []sftp.RequestServerOption{}
		server := sftp.NewRequestServer(
			sess,
			requestHandler,
			serverOptions...,
		)
		if err := server.Serve(); err == io.EOF {
			if err := server.Close(); err != nil {
				logger.Error(
					"unable to close server",
					slog.String("error", err.Error()),
				)
			}
			logger.Info("sftp client exited session.")
		} else if err != nil {
			logger.Error(
				"sftp server completed with error",
				slog.String("error", err.Error()),
			)
		}
		logger.Info("file session completed")
	}
}

func getFileSystemRequestHandler(username string, fileDirectory string) sftp.Handlers {
	baseHandler := &FileSystemRequestHandler{
		fileDirectory: fileDirectory,
		username:      username,
	}
	return sftp.Handlers{
		FileGet:  baseHandler,
		FilePut:  baseHandler,
		FileCmd:  baseHandler,
		FileList: baseHandler,
	}
}

type FileSystemRequestHandler struct {
	fileDirectory string
	username      string
}

func (h *FileSystemRequestHandler) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	dumpRequest(h, r)

	switch r.Method {
	case "Get":
		flags := r.Pflags()
		if !flags.Read {
			return nil, os.ErrInvalid
		}
		fullPath := filepath.Clean(filepath.Join(h.fileDirectory, r.Filepath))
		logger := slog.With(
			slog.String("path", fullPath),
		)
		// The file is opened once and the handle is held for the lifetime of
		// the request. The request server closes it through the io.Closer
		// implementation once the client sends CLOSE.
		file, err := os.OpenFile(fullPath, getFileFlags(flags), defaultFileReadPermission)
		if err != nil {
			return nil, err
		}
		logger.Info("get")
		return &FileSystemReader{file: file}, nil
	default:
		slog.Warn(
			"unsupported method",
			slog.String("method", r.Method),
		)
		return nil, os.ErrInvalid
	}
}

func (h *FileSystemRequestHandler) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	dumpRequest(h, r)

	switch r.Method {
	case "Put":
		flags := r.Pflags()
		if !flags.Write {
			return nil, os.ErrInvalid
		}
		fullPath := filepath.Clean(filepath.Join(h.fileDirectory, r.Filepath))
		slog.Info(
			"put",
			slog.String("path", fullPath),
		)
		filePermission := getWritePermission(r)
		// The file is opened once, so O_CREATE, O_TRUNC and O_EXCL are applied
		// exactly once per transfer. Reopening it per WriteAt would re-truncate
		// the file on every packet and destroy all but the last chunk of any
		// upload larger than one packet.
		file, err := os.OpenFile(fullPath, getFileFlags(flags), filePermission)
		if err != nil {
			return nil, err
		}
		return &FileSystemWriter{file: file}, nil
	case "Open":
		slog.Warn(
			"unsupported method",
			slog.String("method", r.Method),
		)
		return nil, fmt.Errorf("unsupported")
	default:
		return nil, os.ErrInvalid
	}
}

func (h *FileSystemRequestHandler) Filecmd(r *sftp.Request) error {
	dumpRequest(h, r)

	switch r.Method {

	case "Setstat":
		fullPath := filepath.Clean(filepath.Join(h.fileDirectory, r.Filepath))
		logger := slog.With(
			slog.String("path", fullPath),
		)
		logger.Info("Setstat")
		file, err := os.Open(fullPath)
		if err != nil {
			return fmt.Errorf("unable to open file [%s]: %w", fullPath, err)
		}
		// The handle is only used to truncate; a close error on it carries no
		// information about data loss, unlike the transfer handles above.
		defer func() { _ = file.Close() }()
		if r.AttrFlags().Size {
			fileSize := getFileSize(r)
			logger.Info(
				"Setstat",
				slog.Int64("size", fileSize),
			)
			return file.Truncate(fileSize)
		}
		return nil

	case "Rename":
		fullOldPath := filepath.Clean(filepath.Join(h.fileDirectory, r.Filepath))
		fullNewPath := filepath.Clean(filepath.Join(h.fileDirectory, r.Target))
		logger := slog.With(
			slog.String("old_path", fullOldPath),
			slog.String("new_path", fullNewPath),
		)
		logger.Info("Rename")
		if iohelper.IsFileExist(fullNewPath) {
			logger.Info("target file exists")
			return os.ErrExist
		}
		err := os.Rename(fullOldPath, fullNewPath)
		if err != nil {
			return fmt.Errorf("unable to rename file: %w", err)
		}
		return nil

	case "Rmdir":
		fullPath := filepath.Clean(filepath.Join(h.fileDirectory, r.Filepath))
		logger := slog.With(
			slog.String("path", fullPath),
		)
		logger.Info("Rmdir")
		if !iohelper.IsDirectoryExist(fullPath) {
			return os.ErrNotExist
		}
		err := os.RemoveAll(fullPath)
		if err != nil {
			return fmt.Errorf("unable to remove directory: %w", err)
		}
		return nil

	case "Mkdir":
		fullPath := filepath.Clean(filepath.Join(h.fileDirectory, r.Filepath))
		logger := slog.With(
			slog.String("path", fullPath),
		)
		logger.Info("Mkdir")
		if iohelper.IsDirectoryExist(fullPath) {
			return os.ErrExist
		}
		directoryPermission := getWritePermission(r)
		err := os.Mkdir(fullPath, directoryPermission)
		if err != nil {
			return fmt.Errorf("unable to create directory: %w", err)
		}
		return nil

	case "Remove":
		fullPath := filepath.Clean(filepath.Join(h.fileDirectory, r.Filepath))
		logger := slog.With(
			slog.String("path", fullPath),
		)
		logger.Info("Remove")
		if !iohelper.IsFileExist(fullPath) {
			return os.ErrNotExist
		}
		err := os.Remove(fullPath)
		if err != nil {
			return fmt.Errorf("unable to remove file: %w", err)
		}
		return nil

	case "Link", "Symlink", "PosixRename", "StatVFS":
		slog.Warn(
			"unsupported method",
			slog.String("method", r.Method),
		)
		return fmt.Errorf("unsupported")
	default:
		slog.Warn(
			"unsupported method",
			slog.String("method", r.Method),
		)
		return os.ErrInvalid
	}
}

func (h *FileSystemRequestHandler) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	dumpRequest(h, r)

	switch r.Method {

	case "List":
		fullPath := filepath.Clean(filepath.Join(h.fileDirectory, r.Filepath))
		logger := slog.With(
			slog.String("path", fullPath),
		)
		logger.Info("List")
		if !iohelper.IsDirectoryExist(fullPath) {
			return nil, syscall.ENOTDIR
		}
		directoryEntries, err := os.ReadDir(fullPath)
		if err != nil {
			return nil, fmt.Errorf("unable to read directory: %w", err)
		}
		var files []os.FileInfo
		for _, directoryEntry := range directoryEntries {
			f, err := directoryEntry.Info()
			if err != nil {
				return nil, fmt.Errorf("unable to get file info [%s]: %w", directoryEntry.Name(), err)
			}
			files = append(files, f)
		}
		return FileSystemLister(files), nil

	case "Stat":
		fullPath := filepath.Clean(filepath.Join(h.fileDirectory, r.Filepath))
		slog.Info(
			"stat",
			slog.String("path", fullPath),
		)
		fileInfo, err := os.Stat(fullPath)
		if err != nil {
			return nil, err
		}
		return FileSystemLister{fileInfo}, nil
	case "Lstat", "Readlink":
		slog.Warn(
			"unsupported method",
			slog.String("method", r.Method),
		)
		return nil, fmt.Errorf("unsupported")
	default:
		slog.Warn(
			"unsupported method",
			slog.String("method", r.Method),
		)
		return nil, os.ErrInvalid
	}
}

// FileSystemReader reads from a file handle that stays open for the lifetime
// of an SFTP read request.
type FileSystemReader struct {
	file *os.File
}

func (r *FileSystemReader) ReadAt(bytes []byte, offset int64) (n int, err error) {
	if offset < 0 {
		return 0, fmt.Errorf("offset cannot be negative")
	}

	return r.file.ReadAt(bytes, offset)
}

// Close releases the underlying file handle. The request server calls this
// through an io.Closer type assertion when the client closes the handle.
func (r *FileSystemReader) Close() error {
	return r.file.Close()
}

// FileSystemWriter writes to a file handle that stays open for the lifetime of
// an SFTP write request.
type FileSystemWriter struct {
	file *os.File
}

func (w *FileSystemWriter) WriteAt(bytes []byte, offset int64) (n int, err error) {
	if offset < 0 {
		return 0, fmt.Errorf("offset cannot be negative")
	}

	return w.file.WriteAt(bytes, offset)
}

// Close releases the underlying file handle. The request server calls this
// through an io.Closer type assertion and reports any error to the client as
// the result of the CLOSE request, so write-back failures are not lost.
func (w *FileSystemWriter) Close() error {
	return w.file.Close()
}

type FileSystemLister []os.FileInfo

func (f FileSystemLister) ListAt(ls []os.FileInfo, offset int64) (int, error) {
	var n int
	if offset >= int64(len(f)) {
		return 0, io.EOF
	}
	n = copy(ls, f[offset:])
	if n < len(ls) {
		return n, io.EOF
	}
	return n, nil
}

func getFileFlags(flags sftp.FileOpenFlags) int {
	var fileFlags int
	switch {
	case flags.Read && flags.Write:
		// O_RDONLY is zero, so combining it with O_WRONLY would silently yield
		// a write-only handle.
		fileFlags |= os.O_RDWR
	case flags.Write:
		fileFlags |= os.O_WRONLY
	default:
		fileFlags |= os.O_RDONLY
	}
	// O_APPEND is deliberately not translated. It conflicts with WriteAt: the
	// kernel forces every write to end-of-file and ignores the supplied
	// offset, which corrupts transfers that arrive out of order.
	if flags.Creat {
		fileFlags |= os.O_CREATE
	}
	if flags.Trunc {
		fileFlags |= os.O_TRUNC
	}
	if flags.Excl {
		fileFlags |= os.O_EXCL
	}
	return fileFlags
}

func getFileSize(r *sftp.Request) int64 {
	if r.AttrFlags().Size {
		return int64(r.Attributes().Size)
	}
	return 0
}

func getWritePermission(r *sftp.Request) os.FileMode {
	if !r.AttrFlags().Permissions {
		return defaultFileWritePermission
	}
	return r.Attributes().FileMode()
}

func dumpRequest(h *FileSystemRequestHandler, r *sftp.Request) {
	slog.Info(
		"request",
		slog.String("path", r.Filepath),
		slog.String("method", r.Method),
		slog.Group(
			"flags",
			slog.Bool("is_read", r.Pflags().Read),
			slog.Bool("is_write", r.Pflags().Write),
			slog.Bool("is_append", r.Pflags().Append),
			slog.Bool("is_creat", r.Pflags().Creat),
			slog.Bool("is_trunc", r.Pflags().Trunc),
			slog.Bool("is_excl", r.Pflags().Excl),
		),
		slog.Group(
			"attributes",
			slog.Bool("uid_gid", r.AttrFlags().UidGid),
			slog.Bool("acmodtime", r.AttrFlags().Acmodtime),
			slog.Bool("permissions", r.AttrFlags().Permissions),
			slog.Bool("size", r.AttrFlags().Size),
		),
		slog.Group(
			"handler",
			slog.String("directory", h.fileDirectory),
			slog.String("username", h.username),
		),
	)
}
