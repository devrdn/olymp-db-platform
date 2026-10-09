package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
)

// maxBodyBytes bounds a request body; several endpoints need no
// authentication.
const maxBodyBytes = 1 << 20 // 1 MiB

var ErrBadRequest = errors.New("request body is not valid")

// ErrNULText reports a body whose text holds a NUL character, which no stored
// text can contain. It always comes wrapped with ErrBadRequest.
var ErrNULText = errors.New("a text value contains a NUL character")

// ErrBodyTooLarge reports a body over the limit it was read within. It always
// comes wrapped with ErrBadRequest.
var ErrBodyTooLarge = errors.New("body is too large")

// DecodeJSON reads a JSON body into v, refusing anything oversized or
// unexpected. Unknown fields are an error, so a misspelt field is reported
// rather than silently ignored.
func DecodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	return DecodeJSONWithin(w, r, v, maxBodyBytes)
}

// DecodeJSONWithin is DecodeJSON with a tighter bound, enforced as the bytes
// arrive (CLAUDE.md rule 12). A limit above the default is lowered to it.
func DecodeJSONWithin(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, min(limit, maxBodyBytes))

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(v); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return fmt.Errorf("%w: %w", ErrBadRequest, ErrBodyTooLarge)
		}
		return fmt.Errorf("%w: %s", ErrBadRequest, decodeMessage(err))
	}

	// A second document would otherwise be silently dropped.
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: body must contain a single JSON object", ErrBadRequest)
	}

	// JSON may spell a NUL (\u0000), which PostgreSQL refuses with an error.
	// Invalid UTF-8 needs no check: the decoder has already replaced it.
	if holdsNUL(reflect.ValueOf(v)) {
		return fmt.Errorf("%w: %w", ErrBadRequest, ErrNULText)
	}

	return nil
}

// holdsNUL reports whether any string reachable from v contains a NUL.
func holdsNUL(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		return !v.IsNil() && holdsNUL(v.Elem())
	case reflect.String:
		return strings.ContainsRune(v.String(), 0)
	case reflect.Struct:
		// Only exported fields: the decoder fills nothing else.
		fields := v.Type()
		for i := range v.NumField() {
			if fields.Field(i).IsExported() && holdsNUL(v.Field(i)) {
				return true
			}
		}
	case reflect.Slice, reflect.Array:
		// Bytes are not text: whoever decodes a json.RawMessage later owns
		// this check.
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return false
		}
		for i := range v.Len() {
			if holdsNUL(v.Index(i)) {
				return true
			}
		}
	case reflect.Map:
		for _, key := range v.MapKeys() {
			if holdsNUL(key) || holdsNUL(v.MapIndex(key)) {
				return true
			}
		}
	}
	return false
}

func decodeMessage(err error) string {
	var syntax *json.SyntaxError
	var typeErr *json.UnmarshalTypeError

	switch {
	case errors.As(err, &syntax):
		return fmt.Sprintf("malformed JSON at position %d", syntax.Offset)
	case errors.As(err, &typeErr):
		return fmt.Sprintf("field %q has the wrong type", typeErr.Field)
	case errors.Is(err, io.EOF):
		return "body is empty"
	case errors.Is(err, io.ErrUnexpectedEOF):
		// A truncated document arrives as an unexpected EOF, not a SyntaxError.
		return "malformed JSON: the body ends mid-value"
	default:
		return "body could not be read"
	}
}
