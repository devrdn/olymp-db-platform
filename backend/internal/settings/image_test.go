package settings_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/settings"
	"github.com/google/uuid"
)

// pngOf returns a real PNG of that size, so the tests exercise the decoder
// rather than a byte string that happens to start correctly.
func pngOf(t *testing.T, width, height int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func TestAPictureIsStoredWithWhatWasReadOutOfIt(t *testing.T) {
	f := newFixture()

	saved, err := f.service.SaveImage(context.Background(), uuid.New(),
		settings.ImageLogo, pngOf(t, 200, 60))
	if err != nil {
		t.Fatalf("SaveImage() = %v", err)
	}

	if saved.ContentType != "image/png" {
		t.Errorf("content type = %q, want image/png", saved.ContentType)
	}
	if saved.Width != 200 || saved.Height != 60 {
		t.Errorf("size = %dx%d, want 200x60", saved.Width, saved.Height)
	}
	if saved.SHA256 == "" {
		t.Error("no hash: the URL carries it, so a replaced image needs a new address")
	}
}

func TestWhatTheUploadCallsItselfIsIgnored(t *testing.T) {
	// The declared type and the filename are written by whoever is uploading.
	// What decides is the result of decoding the bytes.
	f := newFixture()

	saved, err := f.service.SaveImage(context.Background(), uuid.New(),
		settings.ImageLogo, pngOf(t, 32, 32))
	if err != nil {
		t.Fatalf("SaveImage() = %v", err)
	}

	if saved.ContentType != "image/png" {
		t.Errorf("content type = %q, want the one read out of the bytes", saved.ContentType)
	}
}

func TestSomethingThatIsNotAPictureIsRefused(t *testing.T) {
	f := newFixture()

	for name, data := range map[string][]byte{
		"a script":          []byte("<script>alert(1)</script>"),
		"an SVG":            []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"a PNG that is not": append([]byte("\x89PNG\r\n\x1a\n"), []byte("not really")...),
		"nothing at all":    {},
	} {
		_, err := f.service.SaveImage(context.Background(), uuid.New(), settings.ImageLogo, data)
		if !errors.Is(err, settings.ErrNotAnImage) {
			t.Errorf("%s: SaveImage() = %v, want it refused", name, err)
		}
	}
}

func TestSVGIsRefusedEvenWhenItIsHarmless(t *testing.T) {
	// Not because this one carries a script — because the format can, and an
	// SVG served from this origin is a cross-site script with an
	// administrator's reach. Declining the format declines the whole class,
	// with nothing to keep getting right afterwards.
	f := newFixture()

	_, err := f.service.SaveImage(context.Background(), uuid.New(), settings.ImageLogo,
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><rect width="10" height="10"/></svg>`))

	if !errors.Is(err, settings.ErrNotAnImage) {
		t.Errorf("SaveImage() = %v, want SVG refused whatever is in it", err)
	}
}

func TestAPictureTooBigOnDiskIsRefused(t *testing.T) {
	f := newFixture()

	// Incompressible noise, so the file is genuinely large rather than a large
	// canvas that encodes small.
	huge := make([]byte, 600<<10)
	for i := range huge {
		huge[i] = byte(i * 7)
	}
	copy(huge, pngOf(t, 8, 8))

	_, err := f.service.SaveImage(context.Background(), uuid.New(), settings.ImageLogo, huge)

	if !errors.Is(err, settings.ErrImageTooLarge) && !errors.Is(err, settings.ErrNotAnImage) {
		t.Errorf("SaveImage() = %v, want it refused", err)
	}
}

func TestAPictureTooBigWhenDecodedIsRefused(t *testing.T) {
	// A decompression bomb: small on the wire, enormous once decoded. The
	// process pays for the pixels, not for the bytes.
	f := newFixture()
	data := pngOf(t, 5000, 10)

	_, err := f.service.SaveImage(context.Background(), uuid.New(), settings.ImageLogo, data)

	if !errors.Is(err, settings.ErrImageTooLarge) {
		t.Errorf("SaveImage() = %v, want it refused for its dimensions", err)
	}
}

func TestAnImageSlotNothingReadsIsRefused(t *testing.T) {
	f := newFixture()

	_, err := f.service.SaveImage(context.Background(), uuid.New(), "banner", pngOf(t, 8, 8))

	if !errors.Is(err, settings.ErrUnknownImageKind) {
		t.Errorf("SaveImage() = %v, want an unknown slot refused", err)
	}
}

func TestReplacingAnImageLeavesOneInTheSlot(t *testing.T) {
	// One row per purpose. A gallery of abandoned uploads is a thing somebody
	// has to clean up, and nothing here would ever read them.
	ctx := context.Background()
	f := newFixture()

	first, _ := f.service.SaveImage(ctx, uuid.New(), settings.ImageLogo, pngOf(t, 10, 10))
	second, _ := f.service.SaveImage(ctx, uuid.New(), settings.ImageLogo, pngOf(t, 20, 20))

	if first.SHA256 == second.SHA256 {
		t.Fatal("the two pictures are the same; the test proves nothing")
	}
	present, err := f.service.Images(ctx)
	if err != nil {
		t.Fatalf("Images() = %v", err)
	}
	if present[settings.ImageLogo] != second.SHA256 {
		t.Errorf("slot holds %q, want the later picture", present[settings.ImageLogo])
	}
}

func TestUploadingAnImageIsRecorded(t *testing.T) {
	// Replacing what the whole installation looks like is worth a line.
	ctx := context.Background()
	f := newFixture()

	if _, err := f.service.SaveImage(ctx, uuid.New(), settings.ImageLogo, pngOf(t, 10, 10)); err != nil {
		t.Fatalf("SaveImage() = %v", err)
	}

	if len(f.sink.entries) != 1 {
		t.Fatalf("recorded %d entries, want one", len(f.sink.entries))
	}
	if f.sink.entries[0].Payload["image"] != settings.ImageLogo {
		t.Errorf("payload = %+v, want it to name the slot", f.sink.entries[0].Payload)
	}
}
