package covers_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/covers"
)

func TestASourceTooLargeIsRefusedBeforeItIsDecoded(t *testing.T) {
	// A 100-byte PNG header claiming 30000x30000: decoding it would be 3.6 GB.
	src := pngHeaderClaiming(t, 30000, 30000)

	_, err := covers.Process(bytes.NewReader(src))

	if !errors.Is(err, covers.ErrImageTooLarge) {
		t.Fatalf("Process() = %v, want ErrImageTooLarge", err)
	}
}

func TestAnSVGIsNotAnImageHere(t *testing.T) {
	_, err := covers.Process(strings.NewReader(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
	if !errors.Is(err, covers.ErrImageKind) {
		t.Fatalf("Process() = %v, want ErrImageKind", err)
	}
}

func TestTheOutputCarriesNothingOfTheInputButThePicture(t *testing.T) {
	// A JPEG with an EXIF comment; the re-encoded output must not contain it.
	src := jpegWithComment(t, "Taken at 47.0105, 28.8638")

	out, err := covers.Process(bytes.NewReader(src))
	if err != nil {
		t.Fatalf("Process() = %v", err)
	}

	for _, size := range out.Renditions {
		if bytes.Contains(size.Bytes, []byte("47.0105")) {
			t.Error("the author's location survived into the file we serve")
		}
	}
}

func TestTheOutputIsSixteenByNine(t *testing.T) {
	out, err := covers.Process(bytes.NewReader(jpegOf(t, 1000, 1000)))
	if err != nil {
		t.Fatalf("Process() = %v", err)
	}
	if out.Renditions[0].Width != 1600 || out.Renditions[0].Height != 900 {
		t.Errorf("first rendition is %dx%d, want 1600x900", out.Renditions[0].Width, out.Renditions[0].Height)
	}
}

func TestEveryDeclaredSizeIsProduced(t *testing.T) {
	// Both files are written on the one upload, because the card and the page
	// above the story ask for different ones and neither may discover at read
	// time that its own was never made.
	out, err := covers.Process(bytes.NewReader(jpegOf(t, 1920, 1080)))
	if err != nil {
		t.Fatalf("Process() = %v", err)
	}

	if len(out.Renditions) != len(covers.Sizes) {
		t.Fatalf("Process() made %d renditions, want %d", len(out.Renditions), len(covers.Sizes))
	}
	for i, want := range covers.Sizes {
		got := out.Renditions[i]
		if got.Width != want || got.Height != want*9/16 {
			t.Errorf("rendition %d is %dx%d, want %dx%d", i, got.Width, got.Height, want, want*9/16)
		}
		if got.ContentType != "image/jpeg" {
			t.Errorf("rendition %d is served as %q, want image/jpeg", i, got.ContentType)
		}
	}
}

func TestTheHashNamesTheOutputAndNotTheInput(t *testing.T) {
	// The file's name is the hash of what we wrote, so a browser holding the
	// old cover holds it at an address nothing links to any more. Two uploads
	// of the same picture therefore land on the same name, and a different
	// picture never does.
	same, err := covers.Process(bytes.NewReader(jpegOf(t, 1000, 1000)))
	if err != nil {
		t.Fatalf("Process() = %v", err)
	}
	again, err := covers.Process(bytes.NewReader(jpegOf(t, 1000, 1000)))
	if err != nil {
		t.Fatalf("Process() = %v", err)
	}
	other, err := covers.Process(bytes.NewReader(jpegOf(t, 1600, 400)))
	if err != nil {
		t.Fatalf("Process() = %v", err)
	}

	if same.Hash == "" || same.Hash != again.Hash {
		t.Errorf("the same picture hashed to %q and %q", same.Hash, again.Hash)
	}
	if same.Hash == other.Hash {
		t.Error("two different pictures share a name, so one would be served for the other")
	}
}

func TestABodyOverTheCeilingIsRefusedWithoutBeingHeld(t *testing.T) {
	// The bound is on the socket, not on the decoded value: a reader that
	// allocates first and measures afterwards has already paid for the bytes
	// it exists to keep out (CLAUDE.md, security rule 12).
	oversize := bytes.Repeat([]byte{0xAB}, covers.MaxUploadBytes+1024)
	copy(oversize, jpegOf(t, 8, 8))

	_, err := covers.Process(bytes.NewReader(oversize))

	if !errors.Is(err, covers.ErrTooLarge) {
		t.Fatalf("Process() = %v, want ErrTooLarge", err)
	}
}

func TestAFileWhoseExtensionLiesIsJudgedByItsBytes(t *testing.T) {
	// Nothing about the upload's own claims reaches this function; what
	// decides is the result of sniffing and decoding.
	for name, body := range map[string][]byte{
		"a script":              []byte("<script>alert(1)</script>"),
		"an empty file":         {},
		"a PNG that is not one": append([]byte("\x89PNG\r\n\x1a\n"), []byte("not really")...),
	} {
		if _, err := covers.Process(bytes.NewReader(body)); !errors.Is(err, covers.ErrImageKind) {
			t.Errorf("%s: Process() = %v, want ErrImageKind", name, err)
		}
	}
}
