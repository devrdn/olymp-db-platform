package covers_test

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

// The fixtures below are pictures rather than byte strings that happen to
// start correctly, because what is under test is a decoder's answer and not a
// prefix match. The one exception is pngHeaderClaiming, which is deliberately
// a header with no picture behind it: that is the attack.

// jpegOf returns a real JPEG of that size.
func jpegOf(t *testing.T, width, height int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	// A gradient rather than one flat colour, so that two pictures of
	// different shapes cannot encode to the same bytes and so the resize has
	// something to interpolate.
	for y := range height {
		for x := range width {
			img.Set(x, y, color.RGBA{R: uint8(x % 251), G: uint8(y % 241), B: 0x40, A: 255})
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

// jpegWithComment returns a JPEG carrying comment in a COM segment — the
// shape of everything a camera leaves behind, and the thing re-encoding
// exists to destroy. Spliced in after the two-byte start-of-image marker,
// which is where a writer puts its own metadata.
func jpegWithComment(t *testing.T, comment string) []byte {
	t.Helper()

	body := jpegOf(t, 400, 300)
	if len(body) < 2 {
		t.Fatalf("the encoder produced %d bytes", len(body))
	}

	segment := make([]byte, 0, 4+len(comment))
	segment = append(segment, 0xFF, 0xFE) // COM
	// The length field counts itself and the payload, but not the marker.
	segment = binary.BigEndian.AppendUint16(segment, uint16(2+len(comment)))
	segment = append(segment, comment...)

	out := make([]byte, 0, len(body)+len(segment))
	out = append(out, body[:2]...)
	out = append(out, segment...)
	out = append(out, body[2:]...)

	if !bytes.Contains(out, []byte(comment)) {
		t.Fatal("the fixture does not carry the comment; the test would prove nothing")
	}
	return out
}

// pngHeaderClaiming returns a PNG signature and one IHDR chunk declaring that
// size, and nothing else: no pixel data at all.
//
// It is what a decompression bomb looks like on the wire. image.DecodeConfig
// reads exactly this much and answers with the declared size, which is why
// the refusal can happen before a single pixel is allocated; image.Decode on
// the same bytes would try to build the picture first.
func pngHeaderClaiming(t *testing.T, width, height int) []byte {
	t.Helper()

	header := make([]byte, 13)
	binary.BigEndian.PutUint32(header[0:4], uint32(width))
	binary.BigEndian.PutUint32(header[4:8], uint32(height))
	header[8] = 8 // bit depth
	header[9] = 2 // colour type: truecolour
	// Compression, filter and interlace methods are the only ones PNG defines.

	chunk := make([]byte, 0, 8+len(header)+4)
	chunk = binary.BigEndian.AppendUint32(chunk, uint32(len(header)))
	chunk = append(chunk, "IHDR"...)
	chunk = append(chunk, header...)
	// The checksum covers the type and the data, not the length. A wrong one
	// makes the decoder refuse for the wrong reason.
	chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(chunk[4:]))

	out := append([]byte("\x89PNG\r\n\x1a\n"), chunk...)
	return out
}
