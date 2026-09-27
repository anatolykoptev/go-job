package resource

import "github.com/anatolykoptev/go-panel/csrf"

// csrfFormField is the form-field name the CSRF verifier reads
// (Panel.verifyCSRFToken → csrf.FormField). Templates render their hidden input
// with THIS, never a literal.
//
// resource/detail.templ used to spell it "csrf_token" while the verifier read
// csrf.FormField. The delete form therefore posted a field nothing looked at,
// r.FormValue returned "", and every delete answered 403 — measured against
// production on 2026-08-20. The button rendered, the confirm dialog ran, and
// nothing was ever deleted by it.
//
// A literal in a template is not checkable by the compiler and drifts silently
// in the one direction that looks like a working control. Binding all four
// templates to the constant makes that particular drift impossible rather than
// merely tested for.
const csrfFormField = csrf.FormField
