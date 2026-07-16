package resource

// SaveError is returned by a Writer.Save to surface a domain-level validation
// failure on a specific form field, instead of an opaque internal-server
// error. saveHandler detects it via errors.As (so it unwraps through
// fmt.Errorf("...: %w", err) wrapping) and re-renders the form at HTTP 422
// with Message shown on Field, using the same renderValidationErrors path
// field-level validation already uses — the operator sees an actionable
// message instead of a bare 500.
//
// Field must be an existing key in the resource's FormSpec.Fields; an
// unknown key is simply not displayed anywhere on the re-rendered form.
type SaveError struct {
	// Field is the form field key the error should attach to.
	Field string
	// Message is the user-facing validation message.
	Message string
}

// Error implements the error interface.
func (e *SaveError) Error() string {
	return e.Message
}

// NewSaveError builds a SaveError for the given field with the given
// user-facing message.
func NewSaveError(field, message string) *SaveError {
	return &SaveError{Field: field, Message: message}
}
