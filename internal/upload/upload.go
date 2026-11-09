// Package upload implements the upload flow (STEERING.md §4.3).
package upload

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/tmpfs-code/tmpfs-cli/internal/api"
	"github.com/tmpfs-code/tmpfs-cli/internal/clearance"
	"github.com/tmpfs-code/tmpfs-cli/internal/progress"
	"github.com/tmpfs-code/tmpfs-cli/internal/tmpcrypto"
)

// ChunkSize is the size of each upload request body (a multiple of 16).
// nginx accepts bodies up to 2 MB and the server up to 2.1 MB.
const ChunkSize = 1_800_000

// Options configures an upload.
type Options struct {
	Client      *api.Client
	Path        string
	LifetimeSec int
	PromoCode   string

	// Warnf receives warnings for the user.
	Warnf func(format string, args ...any)
	// NewProgress returns a progress bar (may return nil).
	NewProgress func(label string, total int64) *progress.Bar
}

// ErrNeedsPromo means the upload is in the paid tier and no promo code was given.
var ErrNeedsPromo = errors.New("needs a promo code")

// Run uploads the file and returns the share link.
func Run(ctx context.Context, o Options) (string, error) {
	f, err := os.Open(o.Path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", o.Path)
	}
	size := fi.Size()
	if size == 0 {
		return "", fmt.Errorf("%s is empty", o.Path)
	}
	if size > MaxFileSize {
		return "", fmt.Errorf("%s is too large: %s, the maximum is %s",
			o.Path, progress.FormatBytes(size), progress.FormatBytes(MaxFileSize))
	}

	name := filepath.Base(o.Path)
	maxDownloads := MaxDownloads

	// tier and promo code
	paid := IsPaid(size, o.LifetimeSec, maxDownloads)
	if paid && o.PromoCode == "" {
		return "", fmt.Errorf("%w: %s require a promo code (-p); payments are only available in the webapp",
			ErrNeedsPromo, PaidReason(size, o.LifetimeSec))
	}
	if !paid && o.PromoCode != "" {
		o.warnf("this upload is free, the promo code was not used")
	}

	// crypto material (and metadata, early: it fails on overlong names)
	key, err := tmpcrypto.NewKey()
	if err != nil {
		return "", err
	}
	iv, err := tmpcrypto.NewIV()
	if err != nil {
		return "", err
	}
	x, err := tmpcrypto.EncryptMetadata(key, iv, name, size)
	if err != nil {
		return "", err
	}
	header, err := tmpcrypto.EncodeHeader(x, iv, maxDownloads, o.LifetimeSec)
	if err != nil {
		return "", err
	}

	// file id: from the promo redemption (paid) or random (free)
	var id string
	if paid {
		id, err = o.Client.RedeemPromo(ctx, o.PromoCode, api.PriceReq{
			FilesizeRange:  SizeRange(size),
			MaxDownloads:   maxDownloads,
			ExpireAfterSec: o.LifetimeSec,
			Currency:       "eth",
		})
		if err != nil {
			return "", err
		}
	} else {
		id, err = tmpcrypto.NewID()
		if err != nil {
			return "", err
		}
	}

	// anti-abuse clearance, right before the first chunk
	ch, err := o.Client.Clearance(ctx)
	if err != nil {
		return "", err
	}
	solution, err := clearance.Solve(ctx, ch.Secret, ch.Difficulty)
	if err != nil {
		return "", err
	}

	// encrypt and upload chunk by chunk, with a single reused buffer
	encSize := tmpcrypto.EncryptedSize(size)
	enc := tmpcrypto.NewEncrypter(key, iv)
	buf := make([]byte, ChunkSize)
	src := io.LimitReader(f, size)

	var bar *progress.Bar
	if o.NewProgress != nil {
		bar = o.NewProgress("uploading", size)
	}

	var off, read int64
	for off < encSize {
		want := int64(ChunkSize)
		if rem := encSize - off; rem < want {
			want = rem
		}

		// plaintext bytes in this chunk; the rest is random padding
		plain := want
		if rem := size - read; rem < plain {
			plain = rem
		}
		if _, err := io.ReadFull(src, buf[:plain]); err != nil {
			bar.Clear()
			return "", fmt.Errorf("reading %s: %w (was the file modified during the upload?)", o.Path, err)
		}
		read += plain
		if plain < want {
			if _, err := rand.Read(buf[plain:want]); err != nil {
				return "", err
			}
		}
		enc.CryptBlocks(buf[:want], buf[:want])

		c := api.Chunk{ID: id, From: off, To: off + want, Data: buf[:want]}
		if off == 0 {
			c.Clearance = solution
		}
		if off+want == encSize {
			c.Header = header
		}
		if err := o.Client.UploadChunk(ctx, c); err != nil {
			bar.Clear()
			var se *api.ServerError
			if errors.As(err, &se) && se.Status != 503 && se.Status != 429 {
				return "", fmt.Errorf("upload failed, the server discarded it, please try again: %w", err)
			}
			return "", fmt.Errorf("upload failed: %w", err)
		}

		off += want
		bar.Add(plain)
	}
	bar.Finish()

	// the file must not have grown either
	if n, _ := f.Read(make([]byte, 1)); n != 0 {
		o.warnf("%s changed during the upload, the uploaded copy has its first %d bytes", o.Path, size)
	}

	return tmpcrypto.FormatLink(o.Client.BaseURL(), id, key), nil
}

func (o Options) warnf(format string, args ...any) {
	if o.Warnf != nil {
		o.Warnf(format, args...)
	}
}
