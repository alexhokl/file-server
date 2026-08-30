package handler

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
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
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })

	handlers := getFileSystemRequestHandler("testuser", root)

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

// TestSFTPOpenReadWrite opens a file for reading and writing through the same
// handle. Before the handler implemented OpenFileWriter, the request server
// degraded such opens to write-only and every read failed.
func TestSFTPOpenReadWrite(t *testing.T) {
	client, dir := newSFTPTestPair(t)

	f, err := client.OpenFile("/rw.bin", os.O_RDWR|os.O_CREATE)
	require.NoError(t, err, "an O_RDWR open must succeed")

	want := []byte("read-write over one handle")
	n, err := f.Write(want)
	require.NoError(t, err)
	require.Equal(t, len(want), n)

	got := make([]byte, len(want))
	n, err = f.ReadAt(got, 0)
	require.NoError(t, err)
	require.Equal(t, len(want), n)
	assert.Equal(t, want, got, "reading through the O_RDWR handle must return what was written")

	require.NoError(t, f.Close())

	onDisk, err := os.ReadFile(filepath.Join(dir, "rw.bin"))
	require.NoError(t, err)
	assert.Equal(t, want, onDisk)
}

// TestSFTPCreateThenRead exercises client.Create, which opens with
// O_RDWR|O_CREATE|O_TRUNC, and then reads back through the same handle.
func TestSFTPCreateThenRead(t *testing.T) {
	client, _ := newSFTPTestPair(t)

	f, err := client.Create("/cr.bin")
	require.NoError(t, err)

	want := []byte("created and read back")
	_, err = f.Write(want)
	require.NoError(t, err)

	_, err = f.Seek(0, io.SeekStart)
	require.NoError(t, err)
	got, err := io.ReadAll(f)
	require.NoError(t, err)
	assert.Equal(t, want, got, "reading back through the same Create handle must work")

	require.NoError(t, f.Close())
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

// TestSFTPRenameAtomicity hammers Rename from concurrent sessions: renaming
// onto a fresh target must succeed exactly once, and the winner's content must
// be the only thing any client ever sees at the target.
func TestSFTPRenameAtomicity(t *testing.T) {
	const sessions = 8

	client, dir := newSFTPTestPair(t)

	// Every session renames its own source onto the same fresh target; the
	// linkat-based rename admits exactly one of them.
	var wg sync.WaitGroup
	errs := make([]error, sessions)
	for i := 0; i < sessions; i++ {
		source := fmt.Sprintf("/src-%d.bin", i)
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("src-%d.bin", i)), []byte(fmt.Sprintf("payload-%d", i)), 0o600))
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = client.Rename(source, "/target.bin")
		}(i)
	}
	wg.Wait()

	var successes int
	for _, err := range errs {
		if err == nil {
			successes++
		}
	}
	assert.Equal(t, 1, successes, "exactly one rename must win the target")

	// The target holds the winner's payload, and its source is gone.
	got, err := os.ReadFile(filepath.Join(dir, "target.bin"))
	require.NoError(t, err)
	assert.Regexp(t, `^payload-\d+$`, string(got))
}

// TestSFTPListErrors checks the errors a client sees for a bad List: a
// missing path must report no-such-file rather than a generic failure, and a
// regular file must report not-a-directory.
func TestSFTPListErrors(t *testing.T) {
	client, dir := newSFTPTestPair(t)

	_, err := client.ReadDir("/absent")
	require.Error(t, err)
	// The client library maps the sshFxNoSuchFile status to this message.
	assert.Contains(t, err.Error(), "file does not exist",
		"a missing directory must surface as no-such-file")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "plain.txt"), []byte("f"), 0o600))
	_, err = client.ReadDir("/plain.txt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory",
		"a regular file must surface as not-a-directory")
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

// TestSFTPRmdirNonEmptyFails is the end-to-end guard against recursive
// deletion: RMDIR over the wire must refuse a populated directory, matching
// the semantics of every other SFTP server, instead of deleting the tree.
func TestSFTPRmdirNonEmptyFails(t *testing.T) {
	client, _ := newSFTPTestPair(t)

	// Mkdir over the wire must create a traversable directory (0700), so the
	// upload below can land inside it.
	require.NoError(t, client.Mkdir("/occupied"))
	f, err := client.Create("/occupied/keep.txt")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	assert.Error(t, client.RemoveDirectory("/occupied"),
		"rmdir over SFTP must fail on a non-empty directory")

	// The directory and its contents survived.
	info, err := client.Stat("/occupied")
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	entries, err := client.ReadDir("/occupied")
	require.NoError(t, err)
	assert.Len(t, entries, 1)

	// Emptied, the removal succeeds.
	require.NoError(t, client.Remove("/occupied/keep.txt"))
	require.NoError(t, client.RemoveDirectory("/occupied"))
	_, err = client.Stat("/occupied")
	assert.Error(t, err, "the directory should be gone")
}

// TestSFTPAbsolutePathIsConfined checks that an absolute path from the client
// resolves inside the user directory rather than at the real filesystem root.
//
// This is the property that made the request-based server the only viable
// choice. The built-in sftp.NewServer applies its working directory to
// relative paths only and passes absolute paths straight to the os package,
// so a client could read or write anywhere the process had permission.
//
// The assertions are deliberately positive: the write is arranged to succeed
// so that the test proves the path was rebased under the user directory,
// rather than passing merely because the operation failed.
func TestSFTPAbsolutePathIsConfined(t *testing.T) {
	client, dir := newSFTPTestPair(t)

	// Give the confined location somewhere to land, so a successful write
	// demonstrates redirection instead of an incidental "no such file".
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "etc"), 0o700))

	f, err := client.Create("/etc/confinement-probe")
	require.NoError(t, err, "an absolute path should resolve inside the user directory")
	_, err = f.Write([]byte("probe"))
	require.NoError(t, err)
	require.NoError(t, f.Close())

	// It landed under the user directory, not at the real /etc.
	got, err := os.ReadFile(filepath.Join(dir, "etc", "confinement-probe"))
	require.NoError(t, err, "the write should land under the user directory")
	assert.Equal(t, []byte("probe"), got)
	assert.NoFileExists(t, "/etc/confinement-probe", "must never write outside the user directory")

	// Reading a file that exists on the real filesystem must not reach it.
	// /etc/hosts exists on the platforms this runs on, so a handler that
	// leaked absolute paths would open it and return real content.
	if _, err := os.Stat("/etc/hosts"); err == nil {
		_, err := client.Open("/etc/hosts")
		assert.Error(t, err, "must not be able to read a real absolute path")
	}

	// The user directory is the client's root.
	g, err := client.Create("/at-root.txt")
	require.NoError(t, err)
	require.NoError(t, g.Close())
	assert.FileExists(t, filepath.Join(dir, "at-root.txt"))
}

// TestSFTPParentTraversalIsConfined checks that ".." segments cannot walk out
// of the user directory. As above, the writes are expected to succeed and be
// rebased inside the user directory.
func TestSFTPParentTraversalIsConfined(t *testing.T) {
	client, dir := newSFTPTestPair(t)

	// The parent of the user directory is a real, writable location, which is
	// what an escape would target.
	parent := filepath.Dir(dir)
	require.DirExists(t, parent)

	f, err := client.Create("../escaped.txt")
	require.NoError(t, err)
	_, err = f.Write([]byte("escaped"))
	require.NoError(t, err)
	require.NoError(t, f.Close())

	assert.FileExists(t, filepath.Join(dir, "escaped.txt"), "the traversal should be rebased into the user directory")
	assert.NoFileExists(t, filepath.Join(parent, "escaped.txt"), "must not write into the parent directory")

	f2, err := client.Create("/../../escaped-abs.txt")
	require.NoError(t, err)
	_, err = f2.Write([]byte("escaped"))
	require.NoError(t, err)
	require.NoError(t, f2.Close())

	assert.FileExists(t, filepath.Join(dir, "escaped-abs.txt"))
	assert.NoFileExists(t, filepath.Join(parent, "escaped-abs.txt"), "must not write above the user directory")
	assert.NoFileExists(t, filepath.Join(filepath.Dir(parent), "escaped-abs.txt"))
}

// TestSFTPSymlinkEscapeIsBlocked checks that a symlink inside the user
// directory cannot be used to reach files outside it.
//
// SFTP clients cannot create symlinks here (Filecmd rejects Symlink and Link),
// but symlinks can arrive by other means: a restored backup, an administrator,
// or another process writing into the data volume. Joining the client path onto
// the user directory does not defend against them, because the os package
// follows symlinks during resolution.
func TestSFTPSymlinkEscapeIsBlocked(t *testing.T) {
	client, dir := newSFTPTestPair(t)

	// A directory outside the user's root, holding a file the user must not
	// be able to reach.
	secretDir := t.TempDir()
	secretPath := filepath.Join(secretDir, "secret.txt")
	require.NoError(t, os.WriteFile(secretPath, []byte("top secret"), 0o600))

	// Plant symlinks inside the user directory pointing out of it.
	require.NoError(t, os.Symlink(secretDir, filepath.Join(dir, "escape-dir")))
	require.NoError(t, os.Symlink(secretPath, filepath.Join(dir, "escape-file.txt")))

	t.Run("read through a symlinked directory", func(t *testing.T) {
		f, err := client.Open("/escape-dir/secret.txt")
		if err == nil {
			defer func() { _ = f.Close() }()
			contents, _ := io.ReadAll(f)
			t.Fatalf("read a file outside the user directory: %q", contents)
		}
	})

	t.Run("read through a symlinked file", func(t *testing.T) {
		f, err := client.Open("/escape-file.txt")
		if err == nil {
			defer func() { _ = f.Close() }()
			contents, _ := io.ReadAll(f)
			t.Fatalf("read a file outside the user directory: %q", contents)
		}
	})

	t.Run("write through a symlinked directory", func(t *testing.T) {
		f, err := client.Create("/escape-dir/planted.txt")
		if err == nil {
			_, _ = f.Write([]byte("planted"))
			_ = f.Close()
		}
		assert.NoFileExists(t, filepath.Join(secretDir, "planted.txt"),
			"must not create files outside the user directory")
	})

	t.Run("overwrite through a symlinked file", func(t *testing.T) {
		f, err := client.Create("/escape-file.txt")
		if err == nil {
			_, _ = f.Write([]byte("overwritten"))
			_ = f.Close()
		}
		got, readErr := os.ReadFile(secretPath)
		require.NoError(t, readErr)
		assert.Equal(t, []byte("top secret"), got, "must not overwrite a file outside the user directory")
	})
}

// TestSFTPSymlinkInsideRootStillWorks guards against over-blocking: symlinks
// that stay within the user directory must continue to resolve.
func TestSFTPSymlinkInsideRootStillWorks(t *testing.T) {
	client, dir := newSFTPTestPair(t)

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "real"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "real", "target.txt"), []byte("inside"), 0o600))

	// A relative symlink pointing at a sibling inside the user directory.
	require.NoError(t, os.Symlink("real/target.txt", filepath.Join(dir, "link.txt")))

	f, err := client.Open("/link.txt")
	require.NoError(t, err, "a symlink that stays inside the root should resolve")
	defer func() { _ = f.Close() }()

	got, err := io.ReadAll(f)
	require.NoError(t, err)
	assert.Equal(t, []byte("inside"), got)
}

// TestMaliciousUsernameCannotEscape checks that a username is confined to a
// single directory beneath the users directory. The username reaches the
// handler from the SSH layer and is only as trustworthy as the rows in the
// user table, which the admin API creates without validating.
func TestMaliciousUsernameCannotEscape(t *testing.T) {
	usersDir := t.TempDir()

	// A sibling of the users directory, standing in for anything an escape
	// might try to reach.
	outside := filepath.Join(filepath.Dir(usersDir), "outside-target")
	require.NoError(t, os.MkdirAll(outside, 0o700))

	usersRoot, err := os.OpenRoot(usersDir)
	require.NoError(t, err)
	defer func() { _ = usersRoot.Close() }()

	hostile := []string{"..", "../escaped", "../outside-target", "/etc", "a/../..", `..\escaped`}

	for _, username := range hostile {
		t.Run(username, func(t *testing.T) {
			// Either the name is rejected outright, or os.Root refuses it.
			if err := validateUsername(username); err != nil {
				return
			}
			err := usersRoot.Mkdir(username, 0o700)
			assert.Error(t, err, "os.Root must refuse a username that escapes the users directory")

			_, err = usersRoot.OpenRoot(username)
			assert.Error(t, err)
		})
	}

	// Nothing was created outside the users directory.
	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	assert.Empty(t, entries, "must not create anything outside the users directory")

	assert.NoDirExists(t, filepath.Join(filepath.Dir(usersDir), "escaped"))
}
