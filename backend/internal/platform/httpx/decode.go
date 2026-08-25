package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// maxBodyBytes bounds a request body. Several endpoints are reachable without
// authentication, so an unbounded body is memory anyone may spend.
const maxBodyBytes = 1 << 20 // 1 MiB

// ErrBadRequest reports a body the handler could not use.
var ErrBadRequest = errors.New("request body is not valid")

// DecodeJSON reads a JSON body into v, refusing anything oversized or
// unexpected.
//
// Unknown fields are an error rather than being ignored: a client sending
// "new_pasword" should be told, not silently left with an unchanged password.
func DecodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(v); err != nil {
		return fmt.Errorf("%w: %s", ErrBadRequest, decodeMessage(err))
	}

	// A second value would mean the client sent more than one document; the
	// handler would act on the first and silently drop the rest.
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: body must contain a single JSON object", ErrBadRequest)
	}

	return nil
}

// decodeMessage turns a decoder error into something a client can act on,
// without echoing internals.
func decodeMessage(err error) string {
	var syntax *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	var tooLarge *http.MaxBytesError

	switch {
	case errors.As(err, &tooLarge):
		return "body is too large"
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
