package handler

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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
	handlers := getFileSystemRequestHandler("testuser", dir)

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
