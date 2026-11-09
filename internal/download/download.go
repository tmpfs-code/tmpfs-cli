// Package download implements the download flow (STEERING.md §4.5).
package download

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/tmpfs-code/tmpfs-cli/internal/api"
	"github.com/tmpfs-code/tmpfs-cli/internal/progress"
	"github.com/tmpfs-code/tmpfs-cli/internal/tmpcrypto"
)

// bufSize is the read/decrypt buffer size (a multiple of the AES block size).
const bufSize = 1 << 20

// IdleTimeout aborts a download that receives no data for this long.
var IdleTimeout = 60 * time.Second

// Options configures a download.
type Options struct {
	Client *api.Client
	Link   tmpcrypto.Link
	Output string // file or existing directory; empty: original name in the cwd
	Force  bool

	// Infof receives informational messages for the user.
	Infof func(format string, args ...any)
	// NewProgress returns a progress bar (may return nil).
	NewProgress func(label string, total int64) *progress.Bar
}

// ErrExists means the target file exists and Force is not set.
var ErrExists = errors.New("already exists (use -f to overwrite)")

// Run downloads and decrypts the file and returns the path it was saved to.
func Run(ctx context.Context, o Options) (string, error) {
	id, key := o.Link.ID, o.Link.Key

	// metadata (does not use up a download)
	h, err := o.Client.DownloadHeader(ctx, id)
	if err != nil {
		return "", err
	}
	iv, err := tmpcrypto.ParseIV(h.FM.I)
	if err != nil {
		return "", fmt.Errorf("invalid file metadata: %w", err)
	}
	md, err := tmpcrypto.DecryptMetadata(key, iv, h.FM.X)
	if err != nil {
		return "", err
	}

	// the name is chosen by the uploader: only print its sanitized form, which
	// has no control characters (no terminal escape sequences)
	o.infof("%s (%s), %d %s left, expires in %s",
		SanitizeName(md.Name), progress.FormatBytes(md.Size), h.DownloadsLeft, plural(h.DownloadsLeft, "download"),
		formatDuration(time.Duration(h.SecondsLeft)*time.Second))

	target, err := targetPath(o.Output, md.Name)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(target); err == nil && !o.Force {
		return "", fmt.Errorf("%s %w", target, ErrExists)
	}
	part := target + ".part"
	if _, err := os.Lstat(part); err == nil {
		if !o.Force {
			return "", fmt.Errorf("%s %w", part, ErrExists)
		}
		if err := os.Remove(part); err != nil {
			return "", err
		}
	}

	// body (uses up a download); abort if no data arrives for IdleTimeout
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	body, contentLength, err := o.Client.Download(ctx, id)
	if err != nil {
		return "", err
	}
	defer body.Close()

	encSize := tmpcrypto.EncryptedSize(md.Size)
	if contentLength >= 0 && contentLength != encSize {
		return "", fmt.Errorf("unexpected download size: got %d bytes, want %d", contentLength, encSize)
	}

	// O_EXCL: never write through a file or symlink planted at the .part path
	f, err := os.OpenFile(part, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(part)
		}
	}()

	var bar *progress.Bar
	if o.NewProgress != nil {
		bar = o.NewProgress("downloading", md.Size)
	}

	if err := decryptStream(ctx, cancel, body, f, key, iv, encSize, md.Size, bar); err != nil {
		bar.Clear()
		return "", fmt.Errorf("download failed: %w", err)
	}
	bar.Finish()

	if err := f.Close(); err != nil {
		return "", err
	}
	if o.Force && runtime.GOOS == "windows" {
		// os.Rename does not replace an existing file on Windows
		os.Remove(target)
	}
	if err := os.Rename(part, target); err != nil {
		return "", err
	}
	ok = true
	return target, nil
}

// decryptStream reads exactly encSize bytes of ciphertext from r, decrypts
// them and writes the first size bytes of plaintext to w.
func decryptStream(ctx context.Context, cancel context.CancelCauseFunc, r io.Reader, w io.Writer,
	key tmpcrypto.Key, iv tmpcrypto.IV, encSize, size int64, bar *progress.Bar) error {

	idle := time.AfterFunc(IdleTimeout, func() {
		cancel(fmt.Errorf("no data received for %v", IdleTimeout))
	})
	defer idle.Stop()

	dec := tmpcrypto.NewDecrypter(key, iv)
	bw := bufio.NewWriterSize(w, bufSize)
	buf := make([]byte, bufSize)

	var got, written int64
	for got < encSize {
		want := int64(len(buf))
		if rem := encSize - got; rem < want {
			want = rem
		}
		n, err := io.ReadFull(r, buf[:want])
		if n > 0 {
			idle.Reset(IdleTimeout)
		}
		if err != nil {
			if cause := context.Cause(ctx); cause != nil {
				return cause // idle timeout or interrupted by the user
			}
			if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
				return fmt.Errorf("connection closed after %d of %d bytes", got+int64(n), encSize)
			}
			return err
		}
		got += want

		dec.CryptBlocks(buf[:want], buf[:want])

		out := want
		if rem := size - written; rem < out {
			out = rem
		}
		if out > 0 {
			if _, err := bw.Write(buf[:out]); err != nil {
				return err
			}
			written += out
			bar.Add(out)
		}
	}

	// nothing may follow the expected ciphertext
	if n, _ := r.Read(buf[:1]); n != 0 {
		return errors.New("the server sent more data than expected")
	}
	if written != size {
		return fmt.Errorf("incomplete download: %d of %d bytes", written, size)
	}
	return bw.Flush()
}

// targetPath resolves where to save the file.
func targetPath(output, uploadedName string) (string, error) {
	name := SanitizeName(uploadedName)
	if output == "" {
		return name, nil
	}
	if fi, err := os.Stat(output); err == nil && fi.IsDir() {
		return filepath.Join(output, name), nil
	}
	dir := filepath.Dir(output)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("directory %s does not exist", dir)
	}
	return output, nil
}

func (o Options) infof(format string, args ...any) {
	if o.Infof != nil {
		o.Infof(format, args...)
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

func formatDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return "less than a minute"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%02dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}
