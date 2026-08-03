package resource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/anatolykoptev/go-panel/locale"
	"github.com/anatolykoptev/go-panel/tenant"
)

// FieldKind is the type of form field.
type FieldKind int

const (
	// FieldText is a single-line text input.
	FieldText FieldKind = iota
	// FieldTextarea is a multi-line text area.
	FieldTextarea
	// FieldNumber is a numeric input.
	FieldNumber
	// FieldDate is a date picker (YYYY-MM-DD).
	FieldDate
	// FieldDateTime is a date+time picker (datetime-local). Accepts both the
	// browser's submitted format ("2006-01-02T15:04") and the space-separated
	// human form ("2006-01-02 15:04").
	FieldDateTime
	// FieldSelect is a dropdown with a closed set of options.
	FieldSelect
	// FieldCheckbox is a boolean checkbox.
	FieldCheckbox
	// FieldJSON is a textarea with json.Valid validation.
	FieldJSON
)

// checkboxTrue is the canonical normalised value for a checked checkbox.
const checkboxTrue = "true"

// Option is a single option for a FieldSelect field.
type Option struct {
	Value string
	Label string
}

// Field declares one form field.
//
// For FieldSelect, set exactly one of Options or OptionsFunc:
//   - Options: static list known at init time.
//   - OptionsFunc: called on each form render (GET) and POST validation to
//     provide a fresh list (e.g. from a database query). Mutually exclusive
//     with Options — FormSpec.Valid() enforces this.
type Field struct {
	Key      string
	Label    string
	Kind     FieldKind
	Required bool
	// Translatable marks a field whose value differs per locale (e.g. a title or
	// description). When the deployment configures more than one locale, edit
	// forms render a locale switcher and these fields are edited one locale at a
	// time; the active locale reaches Writer.Load / Writer.Save via context
	// (locale.From(ctx)). Non-translatable fields are locale-independent (e.g. a
	// slug, coordinates, a price) and are edited only on the Default locale.
	// In a single-locale deployment Translatable has no effect.
	Translatable bool
	Options      []Option // static list for FieldSelect; mutually exclusive with OptionsFunc
	// OptionsFunc provides dynamic options for FieldSelect.
	// Called on every form render (GET new/edit) and on every POST validation
	// to build the whitelist. An error causes a 500 — the form is not rendered
	// with a silently empty select.
	// Mutually exclusive with Options.
	OptionsFunc func(ctx context.Context, t tenant.Tenant) ([]Option, error)
	Placeholder string
	Help        string // optional helper text shown below the field
	// Validate is an optional per-field hook run after the Kind-based check
	// passes. Return "" for valid, or a non-empty inline error message.
	// Lets a consumer add domain rules (e.g. int-only) without a new Kind.
	Validate func(val string) string
}

// FormSpec declares the form structure for a Writer.
type FormSpec struct {
	Fields []Field
}

// Valid reports whether the FormSpec is correctly configured.
// Returns a non-nil error for: duplicate keys, empty Key/Label,
// FieldSelect with neither Options nor OptionsFunc, or both set simultaneously.
func (f FormSpec) Valid() error {
	seen := make(map[string]bool, len(f.Fields))
	for i, fld := range f.Fields {
		if fld.Key == "" {
			return fmt.Errorf("resource.FormSpec: field[%d] has empty Key", i)
		}
		if fld.Label == "" {
			return fmt.Errorf("resource.FormSpec: field %q has empty Label", fld.Key)
		}
		if seen[fld.Key] {
			return fmt.Errorf("resource.FormSpec: duplicate field Key %q", fld.Key)
		}
		seen[fld.Key] = true
		if fld.Kind == FieldSelect {
			hasStatic := len(fld.Options) > 0
			hasDynamic := fld.OptionsFunc != nil
			if !hasStatic && !hasDynamic {
				return fmt.Errorf("resource.FormSpec: field %q is FieldSelect but has neither Options nor OptionsFunc", fld.Key)
			}
			if hasStatic && hasDynamic {
				return fmt.Errorf("resource.FormSpec: field %q is FieldSelect with both Options and OptionsFunc — they are mutually exclusive", fld.Key)
			}
		}
	}
	return nil
}

// localeFields returns the fields to render, collect, and validate for the
// active locale.
//
//   - Single-locale (multi=false), or the Default locale, or no active locale:
//     every field is returned (shared + translatable).
//   - A secondary locale: only Translatable fields are returned. Shared fields
//     are locale-independent and edited on the Default locale, so they are
//     neither shown, collected, nor required when translating.
//
// This keeps the rendered form, the POST collection, and validation in lockstep
// for whichever locale is active.
func (fs FormSpec) localeFields(active, def locale.Locale, multi bool) []Field {
	if !multi || active == "" || active == def {
		return fs.Fields
	}
	out := make([]Field, 0, len(fs.Fields))
	for _, fld := range fs.Fields {
		if fld.Translatable {
			out = append(out, fld)
		}
	}
	return out
}

// resolveOptions returns a copy of the FormSpec with all OptionsFunc fields
// resolved into their static Options slice. ctx and t are forwarded to each func.
// Returns an error if any OptionsFunc call fails; the error identifies the field.
func (fs FormSpec) resolveOptions(ctx context.Context, t tenant.Tenant) (FormSpec, error) {
	out := FormSpec{Fields: make([]Field, len(fs.Fields))}
	copy(out.Fields, fs.Fields)
	for i, fld := range out.Fields {
		if fld.Kind != FieldSelect || fld.OptionsFunc == nil {
			continue
		}
		opts, err := fld.OptionsFunc(ctx, t)
		if err != nil {
			return FormSpec{}, fmt.Errorf("resource: OptionsFunc for field %q: %w", fld.Key, err)
		}
		out.Fields[i].Options = opts
		out.Fields[i].OptionsFunc = nil // resolved; no longer needed
	}
	return out, nil
}

// Writer enables create/edit forms for the resource.
// Nil = read-only (Phase 1 behaviour, default).
//
// Locale contract (multi-locale deployments only — see Config.Locales): the
// active locale is carried on ctx; read it with locale.From(ctx). For a
// single-locale deployment locale.From(ctx) is "" and both closures behave
// exactly as before.
type Writer struct {
	Form FormSpec
	// Load returns field values for the edit form. id is the row primary key.
	// On a secondary locale (locale.From(ctx) != "" and != Default) Load should
	// return that locale's Translatable values; shared (non-translatable) values
	// are locale-independent and may be returned as-is or omitted (they are not
	// rendered on a secondary-locale form).
	Load func(ctx context.Context, t tenant.Tenant, id string) (map[string]string, error)
	// Save persists the form. id=="" means create.
	// values are PRE-VALIDATED against Form (required, kind formats, select whitelist).
	// Consumer maps them to SQL.
	//
	// On a secondary locale, values contains ONLY the Translatable fields (shared
	// fields are neither rendered nor collected there). Save MUST therefore MERGE
	// per locale — upsert only the supplied translatable columns for
	// locale.From(ctx) — and MUST NOT issue a full-row replace, or it would null
	// out the shared columns it never received. Create (id=="") and Default-locale
	// saves always carry the full field set.
	Save func(ctx context.Context, t tenant.Tenant, id string, values map[string]string) error
	// Delete removes the row. id is the row primary key (never empty).
	// Nil = no delete route mounted (read-only resource).
	// Soft-delete is the consumer's responsibility — set deleted_at instead of
	// hard-deleting if needed.
	Delete func(ctx context.Context, t tenant.Tenant, id string) error
	// PresetValues returns values to inject on create (id==""). These are
	// merged over form values before Save is called — preset takes precedence.
	// Use for foreign keys (person_id, org_id) that come from context, not the
	// form — prevents hidden-field tampering.
	// Nil = no preset (all values come from the form).
	// Only called on create; ignored on edit.
	PresetValues func(ctx context.Context, t tenant.Tenant) (map[string]string, error)
	// AfterSave is called after Save completes, regardless of error.
	// id is the row ID passed to Save (empty for create). err is nil on success.
	// Hook errors are logged at Warn level and do NOT affect the HTTP response —
	// side-effects are best-effort. Use for cache invalidation, vector re-sync,
	// notifications. Nil = no hook.
	AfterSave func(ctx context.Context, id string, err error)
	// AfterDelete is called after Delete completes, regardless of error.
	// Same contract as AfterSave. Nil = no hook.
	AfterDelete func(ctx context.Context, id string, err error)
	// RedirectAfterSave returns the URL to redirect to after a successful save.
	// nil = redirect to the resource list page (default behaviour).
	// Only used on success — error paths (validation, 500) do not redirect.
	RedirectAfterSave func(ctx context.Context, id string) string
	// RedirectAfterDelete returns the URL to redirect to after a successful delete.
	// nil = redirect to the resource list page (default behaviour).
	RedirectAfterDelete func(ctx context.Context, id string) string
}

// formErrors holds per-field validation errors.
type formErrors map[string]string

// validateFields validates posted form values against the given field set (the
// active locale's fields). Returns formErrors (may be empty on success).
func validateFields(fields []Field, values map[string]string) formErrors {
	errs := make(formErrors)
	for _, fld := range fields {
		val := values[fld.Key]
		if fld.Required && val == "" {
			errs[fld.Key] = fld.Label + " is required"
			continue
		}
		if val == "" {
			continue
		}
		if msg := validateField(fld, val); msg != "" {
			errs[fld.Key] = msg
		}
	}
	return errs
}

// validateField validates a single non-empty field value against its Kind.
// Returns an error message string, or "" if valid.
func validateField(fld Field, val string) string {
	switch fld.Kind {
	case FieldNumber:
		if msg := validateNumber(fld, val); msg != "" {
			return msg
		}
	case FieldDate:
		if msg := validateDate(fld, val); msg != "" {
			return msg
		}
	case FieldDateTime:
		if msg := validateDateTime(fld, val); msg != "" {
			return msg
		}
	case FieldSelect:
		if msg := validateSelect(fld, val); msg != "" {
			return msg
		}
	case FieldJSON:
		if msg := validateJSON(fld, val); msg != "" {
			return msg
		}
	case FieldCheckbox:
		if msg := validateCheckbox(fld, val); msg != "" {
			return msg
		}
	}
	if fld.Validate != nil {
		return fld.Validate(val)
	}
	return ""
}

func validateNumber(fld Field, val string) string {
	var f float64
	if err := json.Unmarshal([]byte(val), &f); err != nil {
		return fld.Label + " must be a number"
	}
	return ""
}

func validateDate(fld Field, val string) string {
	if _, err := time.Parse("2006-01-02", val); err != nil {
		return fld.Label + " must be a valid date (YYYY-MM-DD)"
	}
	return ""
}

// validateDateTime accepts both the browser's datetime-local submission
// format ("2006-01-02T15:04") and the space-separated human form
// ("2006-01-02 15:04").
func validateDateTime(fld Field, val string) string {
	if _, err := time.Parse("2006-01-02T15:04", val); err == nil {
		return ""
	}
	if _, err := time.Parse("2006-01-02 15:04", val); err == nil {
		return ""
	}
	return fld.Label + " must be a valid date and time (YYYY-MM-DD HH:MM)"
}

func validateSelect(fld Field, val string) string {
	for _, opt := range fld.Options {
		if opt.Value == val {
			return ""
		}
	}
	return fld.Label + " is not a valid choice"
}

func validateJSON(fld Field, val string) string {
	if !json.Valid([]byte(val)) {
		return fld.Label + " must be valid JSON"
	}
	return ""
}

func validateCheckbox(fld Field, val string) string {
	switch val {
	case checkboxTrue, "false", "on", "1", "0":
		return ""
	}
	return fld.Label + " must be a boolean"
}

// hasErrors reports whether any field errors exist.
func (fe formErrors) hasErrors() bool {
	return len(fe) > 0
}

// errorsFor is a helper used in templates.
func (fe formErrors) get(key string) string {
	return fe[key]
}

// ErrFormValidation is returned when form validation fails.
// Callers should re-render the form with the validation errors.
var ErrFormValidation = errors.New("form validation failed")
