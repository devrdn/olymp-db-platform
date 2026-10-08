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

// maxBodyBytes bounds a request body. Several endpoints are reachable without
// authentication, so an unbounded body is memory anyone may spend.
const maxBodyBytes = 1 << 20 // 1 MiB

// ErrBadRequest reports a body the handler could not use.
var ErrBadRequest = errors.New("request body is not valid")

// ErrNULText reports a body whose text holds a NUL character, which no
// stored text can contain. Like ErrBodyTooLarge it always comes wrapped
// together with ErrBadRequest, so a handler with a refusal of its own for
// unstorable text (the participant's workspace) can name it, and every other
// handler answers it as any bad body.
var ErrNULText = errors.New("a text value contains a NUL character")

// ErrBodyTooLarge reports a body over the limit it was read within. It always
// comes wrapped together with ErrBadRequest, so a handler that does not care
// why a body was refused need not ask.
var ErrBodyTooLarge = errors.New("body is too large")

// DecodeJSON reads a JSON body into v, refusing anything oversized or
// unexpected.
//
// Unknown fields are an error rather than being ignored: a client sending
// "new_pasword" should be told, not silently left with an unchanged password.
func DecodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	return DecodeJSONWithin(w, r, v, maxBodyBytes)
}

// DecodeJSONWithin is DecodeJSON with a tighter bound than the default, for
// an endpoint whose bodies are small by nature: the bytes are refused as they
// arrive, before anything is decoded (CLAUDE.md rule 12). A body over limit
// is refused with ErrBodyTooLarge as well as ErrBadRequest. A limit above the
// default is lowered to it.
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

	// A second value would mean the client sent more than one document; the
	// handler would act on the first and silently drop the rest.
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: body must contain a single JSON object", ErrBadRequest)
	}

	// JSON may spell a NUL character (\u0000); PostgreSQL cannot store one and
	// refuses it by failing the statement, which would answer the client's
	// own malformed value with a 500. Bytes that are not UTF-8 need no check:
	// the decoder has already replaced them.
	if holdsNUL(reflect.ValueOf(v)) {
		return fmt.Errorf("%w: %w", ErrBadRequest, ErrNULText)
	}

	return nil
}

// holdsNUL reports whether any string reachable from v — a field, an
// element, a map key or value — contains a NUL character.
func holdsNUL(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		return !v.IsNil() && holdsNUL(v.Elem())
	case reflect.String:
		return strings.ContainsRune(v.String(), 0)
	case reflect.Struct:
		// Only what the decoder can have filled: an unexported field (a
		// time's location, say) holds nothing the client sent.
		fields := v.Type()
		for i := range v.NumField() {
			if fields.Field(i).IsExported() && holdsNUL(v.Field(i)) {
				return true
			}
		}
	case reflect.Slice, reflect.Array:
		// Bytes are not text: a json.RawMessage is decoded later, and whoever
		// decodes it owns this check; an identifier is an array of bytes.
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

// decodeMessage turns a decoder error into something a client can act on,
// without echoing internals.
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
		// A truncated document reaches us as an unexpected EOF rather than a
		// SyntaxError; saying so beats a generic "could not be read".
		return "malformed JSON: the body ends mid-value"
	default:
		return "body could not be read"
	}
}
