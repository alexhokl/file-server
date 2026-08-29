package handler

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/pkg/sftp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSFTPTestPair wires a real sftp.Client to a real sftp.RequestServer backed
// by the file system handler, over a loopback TCP connection.
//
// This exercises the full packet path, including the way the request server
// splits transfers larger than one packet across repeated WriteAt/ReadAt calls
// on a single handle. A unix socket is avoided because macOS temporary
// directory paths can exceed the sun_path length limit, and net.Pipe is
// avoided because it is unbuffered.
func newSFTPTestPair(t *testing.T) (*sftp.Client, string) {
	t.Helper()

	dir := t.TempDir()
	handlers := getFileSystemRequestHandler("testuser", dir)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)

		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		server := sftp.NewRequestServer(conn, handlers)
		// Serve returns io.EOF on a clean client disconnect.
		_ = server.Serve()
	}()

	conn, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)

	client, err := sftp.NewClientPipe(conn, conn)
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = client.Close()
		_ = conn.Close()
		_ = listener.Close()
		<-serverDone
	})

	return client, dir
}

// TestSFTPUploadRoundTrip is the end-to-end guard against upload truncation.
//
// Payloads larger than one packet (32 KiB) are split by the request server into
// several WriteAt calls against the same writer. If the writer reopens the file
// with O_TRUNC on each call, every chunk but the last is replaced by zeroes,
// producing a file of the correct length but corrupt contents.
func TestSFTPUploadRoundTrip(t *testing.T) {
	sizes := []struct {
		name string
		size int
	}{
		{name: "empty", size: 0},
		{name: "1KiB", size: 1 << 10},
		{name: "exactly one packet", size: packetSize},
		{name: "one packet plus one byte", size: packetSize + 1},
		{name: "1MiB", size: 1 << 20},
		{name: "5MiB", size: 5 << 20},
	}

	for _, tc := range sizes {
		t.Run(tc.name, func(t *testing.T) {
			client, dir := newSFTPTestPair(t)

			want := make([]byte, tc.size)
			_, err := rand.Read(want)
			require.NoError(t, err)

			f, err := client.Create("/upload.bin")
			require.NoError(t, err)

			n, err := f.Write(want)
			require.NoError(t, err)
			require.Equal(t, tc.size, n)
			require.NoError(t, f.Close())

			got, err := os.ReadFile(filepath.Join(dir, "upload.bin"))
			require.NoError(t, err)

			require.Len(t, got, tc.size, "uploaded file should have the expected length")
			assert.True(t, bytes.Equal(want, got), "uploaded file contents should match the source")
		})
	}
}

func TestSFTPDownloadRoundTrip(t *testing.T) {
	sizes := []struct {
		name string
		size int
	}{
		{name: "empty", size: 0},
		{name: "exactly one packet", size: packetSize},
		{name: "1MiB", size: 1 << 20},
	}

	for _, tc := range sizes {
		t.Run(tc.name, func(t *testing.T) {
			client, dir := newSFTPTestPair(t)

			want := make([]byte, tc.size)
			_, err := rand.Read(want)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "download.bin"), want, 0o600))

			f, err := client.Open("/download.bin")
			require.NoError(t, err)
			defer func() { _ = f.Close() }()

			got, err := io.ReadAll(f)
			require.NoError(t, err)

			require.Len(t, got, tc.size)
			assert.True(t, bytes.Equal(want, got), "downloaded contents should match the source")
		})
	}
}

// TestSFTPOverwriteShrinksFile checks that re-uploading a shorter payload over
// an existing file truncates it once, rather than leaving a tail behind.
func TestSFTPOverwriteShrinksFile(t *testing.T) {
	client, dir := newSFTPTestPair(t)

	large := bytes.Repeat([]byte{'A'}, 3*packetSize)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "overwrite.bin"), large, 0o600))

	small := []byte("small payload")
	f, err := client.Create("/overwrite.bin")
	require.NoError(t, err)
	_, err = f.Write(small)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	got, err := os.ReadFile(filepath.Join(dir, "overwrite.bin"))
	require.NoError(t, err)
	assert.Equal(t, small, got)
}

func TestSFTPListAndRemove(t *testing.T) {
	client, dir := newSFTPTestPair(t)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.txt"), []byte("bb"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sub"), 0o700))

	entries, err := client.ReadDir("/")
	require.NoError(t, err, "listing the user directory over SFTP must succeed")

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.ElementsMatch(t, []string{"a.txt", "b.txt", "sub"}, names)

	require.NoError(t, client.Remove("/a.txt"), "removing an existing file over SFTP must succeed")
	assert.NoFileExists(t, filepath.Join(dir, "a.txt"))

	assert.Error(t, client.Remove("/absent.txt"))
}

func TestSFTPStatAndMkdir(t *testing.T) {
	client, dir := newSFTPTestPair(t)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "stat.txt"), []byte("hello"), 0o600))

	info, err := client.Stat("/stat.txt")
	require.NoError(t, err)
	assert.Equal(t, int64(5), info.Size())

	require.NoError(t, client.Mkdir("/created"))
	created, err := os.Stat(filepath.Join(dir, "created"))
	require.NoError(t, err)
	assert.True(t, created.IsDir())
}
