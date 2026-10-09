package covers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"io"
	"net/http"
	"slices"
	"strings"

	// Registered for their decoders: an accepted format is one that decoded.
	_ "image/jpeg"
	_ "image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"image/jpeg"
)

const (
	// MaxUploadBytes bounds the source on the wire. It equals
	// filestore.MaxFileBytes, so the store never refuses what this writes.
	MaxUploadBytes = 8 << 20

	// MaxSourcePixels bounds each side of the decoded source. It is checked
	// against the header before decoding: a hundred-byte PNG can declare
	// 30000 x 30000, which would decode to 3.6 GB.
	MaxSourcePixels = 8000

	// MaxSourceArea bounds both sides together: 8000 x 8000 passes the side
	// bound yet decodes to about 256 MiB per upload. Forty megapixels is
	// past any camera photograph.
	MaxSourceArea = 40_000_000

	// jpegQuality: above 82 the file grows faster than the picture improves;
	// below it ringing shows under the title.
	jpegQuality = 82

	// The cover's aspect ratio, as integers.
	aspectWidth  = 16
	aspectHeight = 9
)

// Sizes are the widths a cover is stored at, widest first: 1600 above a
// contest's story, 800 in a showcase card.
var Sizes = []int{1600, 800}

// acceptedTypes is the complete set of source formats, decided by reading the
// bytes. Raster only: an SVG carries script, and served from this origin on
// the public front page it would be a cross-site script.
var acceptedTypes = []string{"image/jpeg", "image/png", "image/webp"}

// Rendition is one stored file and what is needed to write and serve it.
type Rendition struct {
	// Size is the requested width, as it appears in the file name.
	Size          int
	Width, Height int
	ContentType   string
	Bytes         []byte
}

type Processed struct {
	// Hash names the output, never the input, so the cached address changes
	// exactly when the stored picture does.
	Hash string
	// Width and Height are the largest rendition's.
	Width, Height int
	Renditions    []Rendition
}

// Process turns an upload into the files this service will serve. Each step
// makes the next affordable:
//
//  1. read at most MaxUploadBytes + 1 bytes (CLAUDE.md rule 12);
//  2. decide the format from the bytes against an allowlist;
//  3. read the header alone and refuse by declared size, before any pixel is
//     allocated;
//  4. decode, crop to 16/9 about the centre and resample;
//  5. encode as JPEG, so nothing hidden in the source survives.
func Process(src io.Reader) (Processed, error) {
	body, err := readBounded(src)
	if err != nil {
		return Processed{}, err
	}

	sniffed, _, _ := strings.Cut(http.DetectContentType(body), ";")
	if !slices.Contains(acceptedTypes, sniffed) {
		return Processed{}, fmt.Errorf("%w: %s", ErrImageKind, sniffed)
	}

	// The header only, so the refusal below costs no decoding.
	config, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		return Processed{}, fmt.Errorf("%w: it does not decode", ErrImageKind)
	}
	if "image/"+format != sniffed {
		return Processed{}, fmt.Errorf("%w: it sniffs as %s and decodes as %s", ErrImageKind, sniffed, format)
	}
	if config.Width <= 0 || config.Height <= 0 {
		return Processed{}, fmt.Errorf("%w: it declares %dx%d", ErrImageKind, config.Width, config.Height)
	}
	if config.Width > 0 && config.Height > MaxSourceArea/config.Width {
		return Processed{}, fmt.Errorf("%w: %d by %d is %d megapixels, and %d is the most this accepts",
			ErrImageTooLarge, config.Width, config.Height,
			config.Width*config.Height/1_000_000, MaxSourceArea/1_000_000)
	}
	if config.Width > MaxSourcePixels || config.Height > MaxSourcePixels {
		return Processed{}, fmt.Errorf("%w: %dx%d, at most %d on a side",
			ErrImageTooLarge, config.Width, config.Height, MaxSourcePixels)
	}

	source, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return Processed{}, fmt.Errorf("%w: it does not decode", ErrImageKind)
	}
	frame := centreCrop(source.Bounds())

	out := Processed{Renditions: make([]Rendition, 0, len(Sizes))}
	// One hash over every rendition, so a change to any of them changes the
	// address of all.
	sum := sha256.New()

	for _, width := range Sizes {
		height := width * aspectHeight / aspectWidth
		dst := image.NewRGBA(image.Rect(0, 0, width, height))
		// CatmullRom: a cheap kernel aliases when reducing by a large factor.
		draw.CatmullRom.Scale(dst, dst.Bounds(), source, frame, draw.Src, nil)

		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: jpegQuality}); err != nil {
			return Processed{}, fmt.Errorf("encode the %dpx rendition: %w", width, err)
		}
		encoded := buf.Bytes()
		sum.Write(encoded)

		out.Renditions = append(out.Renditions, Rendition{
			Size: width, Width: width, Height: height,
			ContentType: "image/jpeg", Bytes: encoded,
		})
	}

	out.Hash = hex.EncodeToString(sum.Sum(nil))
	out.Width, out.Height = out.Renditions[0].Width, out.Renditions[0].Height
	return out, nil
}

// readBounded reads src into memory, refusing anything past the ceiling. It
// reads one byte past the limit to tell an exact-size file from a larger one.
func readBounded(src io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(src, MaxUploadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read the upload: %w", err)
	}
	if len(body) > MaxUploadBytes {
		return nil, fmt.Errorf("%w: at most %d MiB", ErrTooLarge, MaxUploadBytes>>20)
	}
	return body, nil
}

// centreCrop is the widest 16/9 rectangle inside bounds, centred, where a
// photograph's subject usually is.
func centreCrop(bounds image.Rectangle) image.Rectangle {
	width, height := bounds.Dx(), bounds.Dy()

	// Compared as a product, so nothing rounds.
	if width*aspectHeight > height*aspectWidth {
		// Wider than 16/9: trim the width.
		cropped := height * aspectWidth / aspectHeight
		inset := (width - cropped) / 2
		return image.Rect(bounds.Min.X+inset, bounds.Min.Y, bounds.Min.X+inset+cropped, bounds.Max.Y)
	}
	cropped := width * aspectHeight / aspectWidth
	inset := (height - cropped) / 2
	return image.Rect(bounds.Min.X, bounds.Min.Y+inset, bounds.Max.X, bounds.Min.Y+inset+cropped)
}
