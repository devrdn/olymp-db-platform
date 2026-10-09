package settings

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"net/http"
	"slices"
	"strings"
	"time"

	// Registered for their decoders, which prove an upload is a picture.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/google/uuid"
	_ "golang.org/x/image/webp"
)

// Image slots.
const (
	ImageLogo = "logo"
	// ImageIcon is the large square one (home screen, bookmark tile).
	ImageIcon    = "icon"
	ImageFavicon = "favicon"
)

var ImageKinds = []string{ImageLogo, ImageIcon, ImageFavicon}

var (
	ErrUnknownImageKind = errors.New("no such image")
	ErrImageTooLarge    = errors.New("image is too large")
	ErrNotAnImage       = errors.New("file is not an image this installation accepts")
)

// maxImageBytes bounds an upload; half a megabyte is generous for a logo.
const maxImageBytes = 512 << 10

// maxImageEdge bounds the decoded picture, not the file: a small file can
// decode enormous, and the process pays for the pixels.
const maxImageEdge = 4096

// acceptedTypes is what may be stored, decided by reading the bytes.
//
// Raster only. An SVG can carry script, and one served from this origin is a
// cross-site script with an administrator's reach; refusing the format avoids
// sanitising it correctly forever.
var acceptedTypes = []string{"image/png", "image/jpeg", "image/gif", "image/webp"}

type Image struct {
	ID          uuid.UUID
	Kind        string
	ContentType string
	Bytes       []byte
	SHA256      string
	Width       int
	Height      int
	UploadedAt  time.Time
}

type ImageRepository interface {
	// ByKind returns the image in that slot, or ErrImageNotFound.
	ByKind(ctx context.Context, kind string) (Image, error)
	Save(ctx context.Context, actorID uuid.UUID, img Image) error
	Delete(ctx context.Context, kind string) error
	// Present lists the filled slots with the hash their URL carries.
	Present(ctx context.Context) (map[string]string, error)
}

// ErrImageNotFound reports an empty slot.
var ErrImageNotFound = errors.New("no image in that slot")

// inspect decides whether these bytes may be stored, and what they are. The
// upload's Content-Type and filename are ignored; only decoding the bytes is
// trusted.
func inspect(kind string, data []byte) (Image, error) {
	if !slices.Contains(ImageKinds, kind) {
		return Image{}, fmt.Errorf("%w: %q", ErrUnknownImageKind, kind)
	}
	if len(data) == 0 {
		return Image{}, fmt.Errorf("%w: it is empty", ErrNotAnImage)
	}
	if len(data) > maxImageBytes {
		return Image{}, fmt.Errorf("%w: at most %d KiB", ErrImageTooLarge, maxImageBytes>>10)
	}

	// Sniffed first, so what is accepted is what a browser would conclude too;
	// otherwise a file could be stored as one thing and served as another.
	sniffed, _, _ := strings.Cut(http.DetectContentType(data), ";")
	if !slices.Contains(acceptedTypes, sniffed) {
		return Image{}, fmt.Errorf("%w: %s", ErrNotAnImage, sniffed)
	}

	// Then decoded, which cannot be faked by arranging the first bytes, and
	// yields the dimensions without holding the picture in memory.
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return Image{}, fmt.Errorf("%w: it does not decode", ErrNotAnImage)
	}
	if "image/"+format != sniffed {
		return Image{}, fmt.Errorf("%w: it sniffs as %s and decodes as %s", ErrNotAnImage, sniffed, format)
	}
	if config.Width > maxImageEdge || config.Height > maxImageEdge {
		return Image{}, fmt.Errorf("%w: at most %dpx on a side", ErrImageTooLarge, maxImageEdge)
	}

	sum := sha256.Sum256(data)
	return Image{
		Kind:        kind,
		ContentType: sniffed,
		Bytes:       data,
		SHA256:      hex.EncodeToString(sum[:]),
		Width:       config.Width,
		Height:      config.Height,
	}, nil
}
