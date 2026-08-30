package handler

import (
	"bytes"
	"encoding/binary"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/pkg/sftp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SFTP protocol pflag values. These are fixed by the protocol (see RFC draft
// section 6.3) but are unexported in pkg/sftp, so they are redeclared here.
const (
	pflagRead   = 0x00000001
	pflagWrite  = 0x00000002
	pflagAppend = 0x00000004
	pflagCreat  = 0x00000008
	pflagTrunc  = 0x00000010
	pflagExcl   = 0x00000020
)

// sshFileXferAttrSize is the SSH_FILEXFER_ATTR_SIZE attribute flag.
const sshFileXferAttrSize = 0x00000001

// packetSize mirrors defaultMaxTxPacket in pkg/sftp: the maximum payload the
// request server passes to a single WriteAt call. Uploads larger than this are
// split across multiple WriteAt calls on the same writer, which is what
// regressed in the truncation bug.
const packetSize = 1 << 15

// TestMain silences the handler's per-request logging, which is emitted at INFO
// for every operation and otherwise drowns out test failures.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

// newTestHandler returns a handler rooted at a fresh temporary directory,
// along with that directory.
func newTestHandler(t *testing.T) (*FileSystemRequestHandler, string) {
	t.Helper()

	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })

	handlers := getFileSystemRequestHandler("testuser", root)

	h, ok := handlers.FilePut.(*FileSystemRequestHandler)
	require.True(t, ok, "FilePut should be a *FileSystemRequestHandler")

	return h, dir
}

// newRequest builds a request for the given method, path and pflags.
func newRequest(method, path string, flags uint32) *sftp.Request {
	r := sftp.NewRequest(method, path)
	r.Flags = flags
	return r
}

// closeWriter closes a writer returned by Filewrite. The request server closes
// these handles via an io.Closer type assertion, so the writer must implement
// it for buffered data and close errors to reach the client.
func closeWriter(t *testing.T, w io.WriterAt) {
	t.Helper()

	c, ok := w.(io.Closer)
	require.True(t, ok, "writer must implement io.Closer so the request server can close it")
	require.NoError(t, c.Close())
}

// closeReader closes a reader returned by Fileread.
func closeReader(t *testing.T, r io.ReaderAt) {
	t.Helper()

	c, ok := r.(io.Closer)
	require.True(t, ok, "reader must implement io.Closer so the request server can close it")
	require.NoError(t, c.Close())
}

// listAll drains a ListerAt, tolerating the io.EOF that accompanies a short
// final read.
func listAll(t *testing.T, lister sftp.ListerAt, size int) []os.FileInfo {
	t.Helper()

	entries := make([]os.FileInfo, size)
	n, err := lister.ListAt(entries, 0)
	if err != nil && err != io.EOF {
		require.NoError(t, err)
	}
	return entries[:n]
}

func TestFilewrite_MultipleWriteAtDoesNotTruncate(t *testing.T) {
	h, dir := newTestHandler(t)

	// Three full packets, each with a distinct byte pattern, mirroring how the
	// request server splits an upload larger than one packet.
	chunks := [][]byte{
		bytes.Repeat([]byte{'a'}, packetSize),
		bytes.Repeat([]byte{'b'}, packetSize),
		bytes.Repeat([]byte{'c'}, packetSize),
	}
	var want []byte
	for _, c := range chunks {
		want = append(want, c...)
	}

	r := newRequest("Put", "/upload.bin", pflagWrite|pflagCreat|pflagTrunc)
	w, err := h.Filewrite(r)
	require.NoError(t, err)

	// A single writer receives every packet; it must not reopen or re-truncate.
	for i, c := range chunks {
		n, err := w.WriteAt(c, int64(i*packetSize))
		require.NoErrorf(t, err, "WriteAt for chunk %d", i)
		require.Equal(t, len(c), n)
	}

	closeWriter(t, w)

	got, err := os.ReadFile(filepath.Join(dir, "upload.bin"))
	require.NoError(t, err)
	require.Len(t, got, len(want), "file should contain every chunk")
	assert.True(t, bytes.Equal(want, got), "file contents should match what was written")
}

func TestFilewrite_OutOfOrderWriteAt(t *testing.T) {
	h, dir := newTestHandler(t)

	first := bytes.Repeat([]byte{'x'}, packetSize)
	second := bytes.Repeat([]byte{'y'}, packetSize)

	r := newRequest("Put", "/out-of-order.bin", pflagWrite|pflagCreat|pflagTrunc)
	w, err := h.Filewrite(r)
	require.NoError(t, err)

	// The request server dispatches packets across concurrent workers, so a
	// later offset can land before an earlier one.
	_, err = w.WriteAt(second, packetSize)
	require.NoError(t, err)
	_, err = w.WriteAt(first, 0)
	require.NoError(t, err)

	closeWriter(t, w)

	got, err := os.ReadFile(filepath.Join(dir, "out-of-order.bin"))
	require.NoError(t, err)
	require.Len(t, got, 2*packetSize)
	assert.True(t, bytes.Equal(first, got[:packetSize]), "first chunk should survive the later write")
	assert.True(t, bytes.Equal(second, got[packetSize:]), "second chunk should be intact")
}

func TestFilewrite_TruncatesExistingFileExactlyOnce(t *testing.T) {
	h, dir := newTestHandler(t)

	path := filepath.Join(dir, "existing.bin")
	require.NoError(t, os.WriteFile(path, bytes.Repeat([]byte{'o'}, 4096), 0o600))

	payload := []byte("short")
	r := newRequest("Put", "/existing.bin", pflagWrite|pflagCreat|pflagTrunc)
	w, err := h.Filewrite(r)
	require.NoError(t, err)

	_, err = w.WriteAt(payload, 0)
	require.NoError(t, err)
	closeWriter(t, w)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, payload, got, "truncation should happen once, at open")
}

func TestFilewrite_RejectsWithoutWriteFlag(t *testing.T) {
	h, _ := newTestHandler(t)

	r := newRequest("Put", "/nope.bin", pflagRead)
	_, err := h.Filewrite(r)
	assert.ErrorIs(t, err, os.ErrInvalid)
}

func TestFileread_RoundTrip(t *testing.T) {
	h, dir := newTestHandler(t)

	want := bytes.Repeat([]byte{'z'}, packetSize+1)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "download.bin"), want, 0o600))

	r := newRequest("Get", "/download.bin", pflagRead)
	rd, err := h.Fileread(r)
	require.NoError(t, err)

	got := make([]byte, len(want))
	// Read in packet-sized pieces, as the request server does.
	for offset := 0; offset < len(want); offset += packetSize {
		end := offset + packetSize
		if end > len(want) {
			end = len(want)
		}
		n, err := rd.ReadAt(got[offset:end], int64(offset))
		require.NoError(t, err)
		require.Equal(t, end-offset, n)
	}

	closeReader(t, rd)
	assert.True(t, bytes.Equal(want, got))
}

func TestFileread_MissingFileErrorsAtOpen(t *testing.T) {
	h, _ := newTestHandler(t)

	r := newRequest("Get", "/absent.bin", pflagRead)
	_, err := h.Fileread(r)
	require.Error(t, err, "a missing file should fail when the handle is opened")
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// TestOpenFile_ReadWriteRoundTrip covers handles opened for both reading and
// writing: both operations go through the same descriptor.
func TestOpenFile_ReadWriteRoundTrip(t *testing.T) {
	h, dir := newTestHandler(t)

	r := newRequest("Open", "/rw.bin", pflagRead|pflagWrite|pflagCreat|pflagTrunc)
	rw, err := h.OpenFile(r)
	require.NoError(t, err)

	want := []byte("read-write payload")
	n, err := rw.WriteAt(want, 0)
	require.NoError(t, err)
	require.Equal(t, len(want), n)

	got := make([]byte, len(want))
	n, err = rw.ReadAt(got, 0)
	require.NoError(t, err)
	require.Equal(t, len(want), n)
	assert.Equal(t, want, got, "reading through the same handle must return what was written")

	c, ok := rw.(io.Closer)
	require.True(t, ok, "OpenFile handle must implement io.Closer so the request server can close it")
	require.NoError(t, c.Close())

	// The write reached the file system.
	onDisk, err := os.ReadFile(filepath.Join(dir, "rw.bin"))
	require.NoError(t, err)
	assert.Equal(t, want, onDisk)
}

func TestWriteAt_NegativeOffset(t *testing.T) {
	h, _ := newTestHandler(t)

	r := newRequest("Put", "/neg.bin", pflagWrite|pflagCreat|pflagTrunc)
	w, err := h.Filewrite(r)
	require.NoError(t, err)
	defer closeWriter(t, w)

	_, err = w.WriteAt([]byte("data"), -1)
	assert.Error(t, err)
}

func TestReadAt_NegativeOffset(t *testing.T) {
	h, dir := newTestHandler(t)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "neg.bin"), []byte("data"), 0o600))

	r := newRequest("Get", "/neg.bin", pflagRead)
	rd, err := h.Fileread(r)
	require.NoError(t, err)
	defer closeReader(t, rd)

	_, err = rd.ReadAt(make([]byte, 4), -1)
	assert.Error(t, err)
}

func TestGetFileFlags(t *testing.T) {
	tests := []struct {
		name  string
		flags uint32
		want  int
	}{
		{
			name:  "read only",
			flags: pflagRead,
			want:  os.O_RDONLY,
		},
		{
			name:  "write only",
			flags: pflagWrite,
			want:  os.O_WRONLY,
		},
		{
			// O_RDONLY is zero, so a naive bitwise combination would yield a
			// write-only handle and break reads on a read/write handle.
			name:  "read and write maps to O_RDWR",
			flags: pflagRead | pflagWrite,
			want:  os.O_RDWR,
		},
		{
			name:  "typical upload",
			flags: pflagWrite | pflagCreat | pflagTrunc,
			want:  os.O_WRONLY | os.O_CREATE | os.O_TRUNC,
		},
		{
			// pkg/sftp documents that O_APPEND conflicts with WriteAt: it
			// forces every write to end-of-file and ignores the offset.
			name:  "append is stripped because it conflicts with WriteAt",
			flags: pflagWrite | pflagCreat | pflagAppend,
			want:  os.O_WRONLY | os.O_CREATE,
		},
		{
			name:  "append is stripped on an upload that also truncates",
			flags: pflagWrite | pflagCreat | pflagTrunc | pflagAppend,
			want:  os.O_WRONLY | os.O_CREATE | os.O_TRUNC,
		},
		{
			name:  "exclusive create",
			flags: pflagWrite | pflagCreat | pflagExcl,
			want:  os.O_WRONLY | os.O_CREATE | os.O_EXCL,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRequest("Put", "/f", tc.flags)
			assert.Equal(t, tc.want, getFileFlags(r.Pflags()))
		})
	}
}

// TestFilelist_List guards the inverted existence check that previously made
// every successful directory listing return ENOTDIR.
func TestFilelist_List(t *testing.T) {
	h, dir := newTestHandler(t)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "one.txt"), []byte("1"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "two.txt"), []byte("2"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sub"), 0o700))

	lister, err := h.Filelist(newRequest("List", "/", 0))
	require.NoError(t, err, "listing an existing directory must succeed")

	found := listAll(t, lister, 8)
	require.Len(t, found, 3)

	names := make([]string, 0, len(found))
	for _, e := range found {
		names = append(names, e.Name())
	}
	assert.ElementsMatch(t, []string{"one.txt", "two.txt", "sub"}, names)
}

func TestFilelist_ListOnMissingDirectory(t *testing.T) {
	h, _ := newTestHandler(t)

	_, err := h.Filelist(newRequest("List", "/absent", 0))
	require.Error(t, err)
	assert.ErrorIs(t, err, os.ErrNotExist, "a missing directory must surface as no-such-file, not ENOTDIR")
}

// TestFilelist_ListOnFileReturnsENOTDIR guards the exists-but-not-a-directory
// case, which keeps the POSIX errno.
func TestFilelist_ListOnFileReturnsENOTDIR(t *testing.T) {
	h, dir := newTestHandler(t)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "plain.txt"), []byte("f"), 0o600))

	_, err := h.Filelist(newRequest("List", "/plain.txt", 0))
	require.Error(t, err)
	assert.ErrorIs(t, err, syscall.ENOTDIR)
}

func TestFilelist_Stat(t *testing.T) {
	h, dir := newTestHandler(t)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "stat.txt"), []byte("hello"), 0o600))

	lister, err := h.Filelist(newRequest("Stat", "/stat.txt", 0))
	require.NoError(t, err)

	found := listAll(t, lister, 1)
	require.Len(t, found, 1)
	assert.Equal(t, int64(5), found[0].Size())
}

func TestFilecmd_Mkdir(t *testing.T) {
	h, dir := newTestHandler(t)

	require.NoError(t, h.Filecmd(newRequest("Mkdir", "/newdir", 0)))
	info, err := os.Stat(filepath.Join(dir, "newdir"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	// Creating it a second time must be rejected.
	assert.ErrorIs(t, h.Filecmd(newRequest("Mkdir", "/newdir", 0)), os.ErrExist)
}

// TestFilecmd_Remove guards the inverted existence check that previously made
// Remove only ever attempt to delete files that did not exist.
func TestFilecmd_Remove(t *testing.T) {
	h, dir := newTestHandler(t)

	path := filepath.Join(dir, "doomed.txt")
	require.NoError(t, os.WriteFile(path, []byte("bye"), 0o600))

	require.NoError(t, h.Filecmd(newRequest("Remove", "/doomed.txt", 0)), "removing an existing file must succeed")
	assert.NoFileExists(t, path)
}

func TestFilecmd_RemoveMissingFile(t *testing.T) {
	h, _ := newTestHandler(t)

	assert.ErrorIs(t, h.Filecmd(newRequest("Remove", "/absent.txt", 0)), os.ErrNotExist)
}

func TestFilecmd_Rmdir(t *testing.T) {
	h, dir := newTestHandler(t)

	require.NoError(t, os.Mkdir(filepath.Join(dir, "gone"), 0o700))
	require.NoError(t, h.Filecmd(newRequest("Rmdir", "/gone", 0)))
	assert.NoDirExists(t, filepath.Join(dir, "gone"))

	assert.ErrorIs(t, h.Filecmd(newRequest("Rmdir", "/gone", 0)), os.ErrNotExist)
}

// TestFilecmd_RmdirNonEmpty guards the recursive deletion: Rmdir must refuse a
// populated directory instead of removing it with everything inside.
func TestFilecmd_RmdirNonEmpty(t *testing.T) {
	h, dir := newTestHandler(t)

	require.NoError(t, os.Mkdir(filepath.Join(dir, "occupied"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "occupied", "keep.txt"), []byte("data"), 0o600))

	err := h.Filecmd(newRequest("Rmdir", "/occupied", 0))
	require.Error(t, err, "rmdir on a non-empty directory must fail")
	assert.ErrorIs(t, err, syscall.ENOTEMPTY)

	// Nothing was deleted.
	assert.DirExists(t, filepath.Join(dir, "occupied"))
	assert.FileExists(t, filepath.Join(dir, "occupied", "keep.txt"))

	// Once emptied, the same call succeeds.
	require.NoError(t, os.Remove(filepath.Join(dir, "occupied", "keep.txt")))
	require.NoError(t, h.Filecmd(newRequest("Rmdir", "/occupied", 0)))
	assert.NoDirExists(t, filepath.Join(dir, "occupied"))
}

func TestFilecmd_Rename(t *testing.T) {
	h, dir := newTestHandler(t)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "old.txt"), []byte("data"), 0o600))

	r := newRequest("Rename", "/old.txt", 0)
	r.Target = "/new.txt"
	require.NoError(t, h.Filecmd(r))

	assert.NoFileExists(t, filepath.Join(dir, "old.txt"))
	got, err := os.ReadFile(filepath.Join(dir, "new.txt"))
	require.NoError(t, err)
	assert.Equal(t, []byte("data"), got)
}

func TestFilecmd_RenameOntoExistingTarget(t *testing.T) {
	h, dir := newTestHandler(t)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o600))

	r := newRequest("Rename", "/a.txt", 0)
	r.Target = "/b.txt"
	assert.ErrorIs(t, h.Filecmd(r), os.ErrExist, "rename must not clobber an existing file")
}

func TestFilecmd_UnsupportedMethods(t *testing.T) {
	h, _ := newTestHandler(t)

	for _, method := range []string{"Link", "Symlink", "PosixRename", "StatVFS"} {
		t.Run(method, func(t *testing.T) {
			assert.Error(t, h.Filecmd(newRequest(method, "/f", 0)))
		})
	}
}

func TestToRootRelative(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: "."},
		{name: "root", in: "/", want: "."},
		{name: "dot", in: ".", want: "."},
		{name: "simple absolute", in: "/file.txt", want: "file.txt"},
		{name: "simple relative", in: "file.txt", want: "file.txt"},
		{name: "nested", in: "/a/b/c.txt", want: "a/b/c.txt"},
		{name: "trailing slash", in: "/a/b/", want: "a/b"},
		{name: "duplicate separators", in: "//a///b", want: "a/b"},
		{name: "interior dot", in: "/a/./b", want: "a/b"},
		{name: "interior parent", in: "/a/../b", want: "b"},
		{name: "leading parent", in: "../etc/passwd", want: "etc/passwd"},
		{name: "absolute parent", in: "/../etc/passwd", want: "etc/passwd"},
		{name: "many parents", in: "/a/../../../../etc/passwd", want: "etc/passwd"},
		{name: "only parents", in: "../../..", want: "."},
		{name: "parent of root", in: "/..", want: "."},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := toRootRelative(tc.in)
			assert.Equal(t, tc.want, got)

			// Whatever the input, the result must never be absolute or start
			// with a parent reference, which os.Root would reject outright.
			assert.False(t, strings.HasPrefix(got, "/"), "result must not be absolute")
			assert.False(t, got == ".." || strings.HasPrefix(got, "../"), "result must not escape")
		})
	}
}

func TestValidateUsername(t *testing.T) {
	valid := []string{"alice", "bob.smith", "user-1", "UPPER", "a_b", "user@example.com"}
	for _, username := range valid {
		t.Run("valid/"+username, func(t *testing.T) {
			assert.NoError(t, validateUsername(username))
		})
	}

	invalid := []string{"", ".", "..", "../evil", "a/b", "/etc", "etc/", `back\slash`, "nul\x00byte"}
	for _, username := range invalid {
		t.Run("invalid/"+username, func(t *testing.T) {
			assert.ErrorIs(t, validateUsername(username), errInvalidUsername)
		})
	}
}

// TestContainmentDoesNotDependOnSFTPCleaning feeds the handler request paths
// that pkg/sftp never cleaned.
//
// sftp.NewRequest runs the path through the library's own cleaning, so tests
// built on it cannot distinguish our containment from the library's. These
// requests set Filepath directly, which is what a different SFTP library, a
// future change upstream, or any other caller of this handler could produce.
func TestContainmentDoesNotDependOnSFTPCleaning(t *testing.T) {
	// A file outside the user directory that must stay unreachable.
	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "secret.txt")
	require.NoError(t, os.WriteFile(outsidePath, []byte("top secret"), 0o600))

	// Raw, uncleaned paths, including an absolute path to the real file.
	hostile := []string{
		"../secret.txt",
		"../../secret.txt",
		"a/../../secret.txt",
		"/../secret.txt",
		filepath.Join(outsideDir, "secret.txt"),
		outsidePath,
	}

	for _, requestPath := range hostile {
		t.Run(requestPath, func(t *testing.T) {
			h, dir := newTestHandler(t)

			// Bypass sftp.NewRequest's cleaning by setting Filepath directly.
			read := &sftp.Request{Method: "Get", Filepath: requestPath, Flags: pflagRead}
			if _, err := h.Fileread(read); err == nil {
				t.Fatalf("read escaped the user directory via %q", requestPath)
			}

			write := &sftp.Request{
				Method:   "Put",
				Filepath: requestPath,
				Flags:    pflagWrite | pflagCreat | pflagTrunc,
			}
			if w, err := h.Filewrite(write); err == nil {
				_, _ = w.WriteAt([]byte("planted"), 0)
				if c, ok := w.(io.Closer); ok {
					_ = c.Close()
				}
			}

			// The outside file is untouched and nothing was planted next to it.
			got, err := os.ReadFile(outsidePath)
			require.NoError(t, err)
			assert.Equal(t, []byte("top secret"), got, "must not modify a file outside the user directory")

			// Anything that did get created stayed inside the user directory.
			assert.NoFileExists(t, filepath.Join(filepath.Dir(dir), "secret.txt"))
		})
	}
}

func TestFilecmd_SetstatTruncate(t *testing.T) {
	h, dir := newTestHandler(t)

	path := filepath.Join(dir, "truncate.txt")
	require.NoError(t, os.WriteFile(path, []byte("0123456789"), 0o600))

	// For SETSTAT the attribute flags live in Request.Flags, and Attrs holds
	// only the values: here a single 64-bit size.
	attrs := make([]byte, 8)
	binary.BigEndian.PutUint64(attrs, 4)

	r := newRequest("Setstat", "/truncate.txt", sshFileXferAttrSize)
	r.Attrs = attrs
	require.True(t, r.AttrFlags().Size, "the request should carry a size attribute")
	require.Equal(t, int64(4), getFileSize(r))

	require.NoError(t, h.Filecmd(r), "Setstat with a size should truncate the file")

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, []byte("0123"), got)
}
