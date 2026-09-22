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

	// Registered for their decoders. A format this service accepts is a format
	// it can decode, and a format it can decode is one whose bytes have been
	// read rather than whose name has been believed.
	_ "image/jpeg"
	_ "image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"image/jpeg"
)

// What a cover may arrive as, and what it becomes.
const (
	// MaxUploadBytes bounds the source on the wire. Eight mebibytes is a
	// photograph off a camera with room to spare, and it is the same ceiling
	// filestore.MaxFileBytes names, so nothing this package writes can be
	// refused by the store it writes to.
	MaxUploadBytes = 8 << 20

	// MaxSourcePixels bounds each side of the *decoded* source.
	//
	// It is checked against the header rather than against a decoded picture,
	// which is the whole point: a hundred-byte PNG can declare 30000 × 30000,
	// and building that picture is 3.6 GB in the process serving the
	// olympiad. Eight thousand is above anything a camera or a phone
	// produces and two orders of magnitude below the declaration that
	// attack has to make to be worth making.
	MaxSourcePixels = 8000

	// MaxSourceArea bounds the two sides together, because the side bound
	// alone does not bound the memory: 8000 x 8000 is inside it and is
	// 64 megapixels, which Go decodes as roughly 256 MiB of RGBA — per
	// upload, with nothing making two of them wait for each other. Forty
	// megapixels is past any photograph a camera hands to an organiser
	// (a 24 MP frame is 6000 x 4000) and an eighth of the earlier ceiling.
	MaxSourceArea = 40_000_000

	// jpegQuality is what every stored rendition is written at. 82 is the
	// usual knee of the curve for photographs: above it the file grows
	// faster than the picture improves, below it the ringing shows on the
	// flat areas a cover's title sits over.
	jpegQuality = 82

	// The shape every cover is cut to, as a pair rather than a float so the
	// arithmetic below stays in integers.
	aspectWidth  = 16
	aspectHeight = 9
)

// Sizes are the widths a cover is stored at, widest first.
//
// Two, and no more: 1600 is the picture above a contest's story, 800 is the
// one in a showcase card. Sending a 1600-pixel file into a card 400 pixels
// wide spends a school's Wi-Fi on detail nobody can see, and a third size
// between them would be a third file to write, back up and sweep for a
// difference no visitor could point at.
var Sizes = []int{1600, 800}

// acceptedTypes is the complete set of source formats, decided by reading the
// bytes.
//
// Raster only. An SVG is a document, not a picture: it carries script and
// external references, and one served from this origin to every visitor of
// the front page — none of whom has signed in — is a cross-site script on the
// most public surface the installation has. There are ways to make one safe,
// and each of them is a thing that has to keep being right; declining the
// format declines the whole class once.
var acceptedTypes = []string{"image/jpeg", "image/png", "image/webp"}

// Rendition is one stored file: the bytes, and everything the store and the
// HTTP layer need to write and serve them.
type Rendition struct {
	// Size is the width this rendition was asked for, and the number that
	// appears in its file name.
	Size          int
	Width, Height int
	ContentType   string
	Bytes         []byte
}

// Processed is the result of one upload: every rendition, and the name they
// are stored under.
type Processed struct {
	// Hash names the *output*, never the input. The address a browser caches
	// forever therefore changes exactly when the picture it holds changes,
	// and two uploads of the same photograph do not make two sets of files.
	Hash string
	// Width and Height are the largest rendition's, which is what the
	// database records about the cover.
	Width, Height int
	Renditions    []Rendition
}

// Process turns whatever an organiser uploaded into the files this service
// will serve.
//
// The order of the steps is the order of the defences, and each one exists to
// make the next one affordable:
//
//  1. read at most MaxUploadBytes + 1 bytes, so an endless body costs a
//     bounded amount of memory (CLAUDE.md, security rule 12);
//  2. decide the format from the bytes, against an explicit allowlist, so
//     nothing the upload calls itself is ever consulted;
//  3. read the header alone and refuse by declared size, so a decompression
//     bomb is refused before a pixel is allocated;
//  4. decode, crop to 16/9 about the centre and resample;
//  5. encode as JPEG.
//
// Step 5 is what makes the output safe rather than merely well-formed: the
// file handed to the store is one this process wrote, so nothing that was
// hiding in the source — the author's location in an EXIF tag, a second
// document appended after the end of the picture, a polyglot that is also
// valid HTML — is in it. Sanitising the input would be the other approach,
// and it is a list of things to keep remembering; this is not.
func Process(src io.Reader) (Processed, error) {
	body, err := readBounded(src)
	if err != nil {
		return Processed{}, err
	}

	sniffed, _, _ := strings.Cut(http.DetectContentType(body), ";")
	if !slices.Contains(acceptedTypes, sniffed) {
		return Processed{}, fmt.Errorf("%w: %s", ErrImageKind, sniffed)
	}

	// The header only. DecodeConfig reads as far as the dimensions and stops,
	// so the refusal below costs the first few hundred bytes of the file and
	// not the picture it claims to be.
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
	// One hash over every rendition in order, rather than over the widest
	// one: the set of files is what a cover is, and a change in how any of
	// them is produced has to change the address, or a browser holding
	// yesterday's 800-pixel file would keep it against today's 1600-pixel one.
	sum := sha256.New()

	for _, width := range Sizes {
		height := width * aspectHeight / aspectWidth
		dst := image.NewRGBA(image.Rect(0, 0, width, height))
		// CatmullRom rather than a box filter: a cover is a photograph
		// reduced by a large factor, which is exactly where a cheap kernel
		// shows as aliasing along every hard edge in it.
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

// readBounded reads src into memory, refusing anything past the ceiling.
//
// One byte past the limit rather than the limit itself, because a reader that
// stops exactly at it cannot tell a file of exactly MaxUploadBytes from a
// larger one it truncated — and silently serving the first eight mebibytes of
// a bigger upload is worse than refusing it.
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

// centreCrop is the widest 16/9 rectangle inside bounds, centred.
//
// Centred rather than anchored to an edge because a photograph's subject is
// ordinarily in the middle of it, and because the showcase card lays a scrim
// over the lower third: a crop that kept the top would put a sky behind the
// title and a crop that kept the bottom would put the subject under it.
func centreCrop(bounds image.Rectangle) image.Rectangle {
	width, height := bounds.Dx(), bounds.Dy()

	// Compared as a product rather than a ratio, so nothing here rounds.
	if width*aspectHeight > height*aspectWidth {
		// Wider than 16/9: the height is what fits, and the width is trimmed.
		cropped := height * aspectWidth / aspectHeight
		inset := (width - cropped) / 2
		return image.Rect(bounds.Min.X+inset, bounds.Min.Y, bounds.Min.X+inset+cropped, bounds.Max.Y)
	}
	cropped := width * aspectHeight / aspectWidth
	inset := (height - cropped) / 2
	return image.Rect(bounds.Min.X, bounds.Min.Y+inset, bounds.Max.X, bounds.Min.Y+inset+cropped)
}
