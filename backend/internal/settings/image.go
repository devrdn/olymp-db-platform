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

	// Registered for their decoders, which is what proves an upload is the
	// picture it claims to be rather than a file with a picture's first bytes.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/google/uuid"
	_ "golang.org/x/image/webp"
)

// What an installation may replace about how it looks.
const (
	// ImageLogo is the mark in the bar and on the sign-in screen.
	ImageLogo = "logo"
	// ImageIcon is the large square one — a home screen, a bookmark tile.
	ImageIcon = "icon"
	// ImageFavicon is the small square one in a browser tab.
	ImageFavicon = "favicon"
)

// ImageKinds is every slot, in the order a screen offers them.
var ImageKinds = []string{ImageLogo, ImageIcon, ImageFavicon}

// Errors an upload can produce.
var (
	ErrUnknownImageKind = errors.New("no such image")
	ErrImageTooLarge    = errors.New("image is too large")
	ErrNotAnImage       = errors.New("file is not an image this installation accepts")
)

// maxImageBytes bounds an upload. A logo is a logo: half a megabyte is
// generous for one, and the bound is what stops the branding screen becoming
// somewhere to park a file.
const maxImageBytes = 512 << 10

// maxImageEdge bounds the decoded picture, not the file.
//
// A small file can decode enormous — that is what a decompression bomb is —
// and the process pays for the pixels, not for the bytes on the wire.
const maxImageEdge = 4096

// acceptedTypes is what may be stored, decided by reading the bytes.
//
// Raster only, and SVG deliberately absent. An SVG is an executable document:
// it carries script and event handlers, and one served from this origin is a
// cross-site script with an administrator's reach. There are ways to make it
// safe — sanitise on the way in, refuse to serve it as a document — and each
// is a thing that has to keep being right. A logo at twice the size loses
// nothing anybody will see, so the whole class of problem is declined instead.
var acceptedTypes = []string{"image/png", "image/jpeg", "image/gif", "image/webp"}

// Image is one stored picture.
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

// ImageRepository stores the pictures.
type ImageRepository interface {
	// ByKind returns the image in that slot, or ErrImageNotFound.
	ByKind(ctx context.Context, kind string) (Image, error)
	// Save replaces whatever is in the slot.
	Save(ctx context.Context, actorID uuid.UUID, img Image) error
	// Delete empties the slot.
	Delete(ctx context.Context, kind string) error
	// Present lists the slots that hold something, with the hash the URL
	// carries, so a page can link them without reading the bytes.
	Present(ctx context.Context) (map[string]string, error)
}

// ErrImageNotFound reports an empty slot.
var ErrImageNotFound = errors.New("no image in that slot")

// inspect decides whether these bytes may be stored, and what they are.
//
// The upload's own Content-Type and filename are ignored entirely: both are
// written by whoever is uploading. What is trusted is the result of decoding
// the bytes — a file that decodes as a PNG is a PNG, whatever it was called.
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

	// Sniffed first, so an accepted answer here is what the browser would also
	// conclude — the two disagreeing is how a file gets stored as one thing
	// and served as another.
	sniffed, _, _ := strings.Cut(http.DetectContentType(data), ";")
	if !slices.Contains(acceptedTypes, sniffed) {
		return Image{}, fmt.Errorf("%w: %s", ErrNotAnImage, sniffed)
	}

	// Then decoded, which is the part that cannot be faked by arranging the
	// first few bytes. It also yields the dimensions without holding the whole
	// picture in memory.
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
