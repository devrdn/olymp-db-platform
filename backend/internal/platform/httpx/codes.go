package httpx

// The codes the HTTP plumbing itself answers with. Everything above it
// declares its own next to the handlers that emit them, so a code and the
// situation it describes stay in one place.
var (
	// CodeInternalError is the only code that means "we do not know". It is
	// declared here because every layer needs it and none of them should
	// declare a second spelling of it.
	CodeInternalError = NewCode("internal_error",
		"The server failed in a way it could not attribute. The detail is in the log under this response's request id.")

	// CodeCrossOrigin guards cookie-borne sessions on mutating requests.
	CodeCrossOrigin = NewCode("cross_origin",
		"The request carried an Origin the server does not serve. Sent for a write that a browser made from another site.")
)
