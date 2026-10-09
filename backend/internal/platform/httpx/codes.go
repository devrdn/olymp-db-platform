package httpx

// The codes the HTTP plumbing itself answers with. Other codes are declared
// next to the handlers that emit them.
var (
	// CodeInternalError is the only code that means "we do not know".
	CodeInternalError = NewCode("internal_error",
		"The server failed in a way it could not attribute. The detail is in the log under this response's request id.")

	CodeCrossOrigin = NewCode("cross_origin",
		"The request carried an Origin the server does not serve. Sent for a write that a browser made from another site.")
)
