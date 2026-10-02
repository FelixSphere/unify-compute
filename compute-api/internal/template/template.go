// Package template holds the curated job templates: a named, reviewed job
// spec a customer can submit by id instead of writing one. Templates ship with
// the binary like the SKU catalogue, so a change to what a curated template
// runs is a reviewable diff, and a template that names a SKU the catalogue does
// not have, or an image that is not pinned, stops the process at boot.
package template

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
)

// Mount is a path inside the job's container. A template declares its mounts;
// the submitter supplies a source for each input mount.
type Mount struct {
	Mount       string `json:"mount"`
	Description string `json:"description"`
}

type Template struct {
	ID                string            `json:"id"`
	Object            string            `json:"object"`
	Title             string            `json:"title"`
	Description       string            `json:"description"`
	Image             string            `json:"image"`
	ImageLicence      string            `json:"image_licence,omitempty"`
	ImageSource       string            `json:"image_source,omitempty"`
	Command           []string          `json:"command"`
	Env               map[string]string `json:"env"`
	SKU               string            `json:"sku"`
	MaxRuntimeSeconds int64             `json:"max_runtime_seconds"`
	Inputs            []Mount           `json:"inputs"`
	Outputs           []Mount           `json:"outputs"`
	Overridable       []string          `json:"overridable"`
	// Available is what the file declares until Parse, and afterwards what is
	// true: declared available and the default SKU is sold.
	Available         bool   `json:"available"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

type Set struct {
	Note      string     `json:"note,omitempty"`
	Templates []Template `json:"templates"`
}

// Field names a submit may override, as request paths. image, inputs and
// outputs are deliberately absent: a different image is a different template,
// and the declared mounts are the template's contract with its command. A
// template may only list fields from this set.
const (
	FieldSKU               = "sku"
	FieldProviders         = "providers"
	FieldCommand           = "spec.command"
	FieldEnv               = "spec.env"
	FieldMaxRuntimeSeconds = "spec.max_runtime_seconds"
)

var overridableFields = map[string]bool{
	FieldSKU: true, FieldProviders: true, FieldCommand: true, FieldEnv: true, FieldMaxRuntimeSeconds: true,
}

var (
	ErrUnknown = errors.New("unknown template")

	idRe     = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

	// imageRe is the distribution reference grammar (registry, path, optional
	// tag) with the digest made mandatory. Digest because a tag is a moving
	// pointer: Docker Hub lets a publisher re-push v0.30.0, and the reviewed
	// diff would no longer be what runs. The tag stays for the human reading
	// the file; the digest is what is pulled.
	imageRe = regexp.MustCompile(`^` +
		`(?:(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?)*(?::[0-9]+)?)/)?` +
		`[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*` +
		`(?::(?P<tag>[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}))?` +
		`@sha256:[a-f0-9]{64}$`)
)

// ValidImage reports whether ref is pinned by digest. A `latest` tag is refused
// even with a digest, because the tag then tells the reader something false.
func ValidImage(ref string) error {
	m := imageRe.FindStringSubmatch(ref)
	if m == nil {
		return fmt.Errorf("image %q is not a reference pinned by @sha256 digest", ref)
	}
	if m[imageRe.SubexpIndex("tag")] == "latest" {
		return fmt.Errorf("image %q: tag latest is misleading next to a digest; name the release", ref)
	}
	return nil
}

func Load(file string, cat *sku.Catalogue, ceiling int64) (*Set, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	return Parse(raw, cat, ceiling)
}

// Parse validates every template against the catalogue it will be sold from
// and the platform runtime ceiling. ceiling is passed in rather than imported
// because the API package owns it and imports this one.
func Parse(raw []byte, cat *sku.Catalogue, ceiling int64) (*Set, error) {
	var s Set
	dec := json.NewDecoder(bytes.NewReader(raw))
	// A typo in a reviewed file ("overrideable") must not silently mean "no
	// overrides".
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("templates: %w", err)
	}
	seen := map[string]bool{}
	for i := range s.Templates {
		t := &s.Templates[i]
		if !idRe.MatchString(t.ID) {
			return nil, fmt.Errorf("templates: id %q is not a lowercase slug", t.ID)
		}
		if seen[t.ID] {
			return nil, fmt.Errorf("templates: duplicate template %q", t.ID)
		}
		seen[t.ID] = true
		if err := t.validate(cat, ceiling); err != nil {
			return nil, fmt.Errorf("template %q: %w", t.ID, err)
		}
		t.Object = "compute.template"
	}
	sort.Slice(s.Templates, func(i, j int) bool { return s.Templates[i].ID < s.Templates[j].ID })
	return &s, nil
}

func (t *Template) validate(cat *sku.Catalogue, ceiling int64) error {
	if strings.TrimSpace(t.Title) == "" || strings.TrimSpace(t.Description) == "" {
		return errors.New("title and description are required")
	}
	switch {
	case t.Image != "":
		if err := ValidImage(t.Image); err != nil {
			return err
		}
	case t.Available:
		return errors.New("available template has no image")
	}
	if t.Available && len(t.Command) == 0 {
		return errors.New("available template has no command")
	}
	if !t.Available && strings.TrimSpace(t.UnavailableReason) == "" {
		return errors.New("unavailable template must say why in unavailable_reason")
	}
	item, err := cat.Get(t.SKU)
	if err != nil {
		return fmt.Errorf("default sku %q is not in the catalogue", t.SKU)
	}
	if t.MaxRuntimeSeconds <= 0 || t.MaxRuntimeSeconds > ceiling {
		return fmt.Errorf("max_runtime_seconds %d must be in (0, %d]", t.MaxRuntimeSeconds, ceiling)
	}
	for k := range t.Env {
		if !envKeyRe.MatchString(k) {
			return fmt.Errorf("env key %q is not a shell identifier", k)
		}
	}
	mounts := map[string]bool{}
	for _, m := range append(append([]Mount{}, t.Inputs...), t.Outputs...) {
		if !path.IsAbs(m.Mount) || path.Clean(m.Mount) != m.Mount || m.Mount == "/" {
			return fmt.Errorf("mount %q must be a clean absolute path below /", m.Mount)
		}
		for other := range mounts {
			if other == m.Mount || strings.HasPrefix(m.Mount, other+"/") || strings.HasPrefix(other, m.Mount+"/") {
				return fmt.Errorf("mounts %q and %q overlap", other, m.Mount)
			}
		}
		mounts[m.Mount] = true
	}
	over := map[string]bool{}
	for _, f := range t.Overridable {
		if !overridableFields[f] {
			return fmt.Errorf("field %q cannot be overridable", f)
		}
		if over[f] {
			return fmt.Errorf("overridable field %q listed twice", f)
		}
		over[f] = true
	}
	if t.Env == nil {
		t.Env = map[string]string{}
	}
	if t.Available && !item.Available {
		t.Available = false
		t.UnavailableReason = fmt.Sprintf("default sku %q is not currently sold", t.SKU)
	}
	return nil
}

func (s *Set) Get(id string) (Template, error) {
	if s != nil {
		for _, t := range s.Templates {
			if t.ID == id {
				return t, nil
			}
		}
	}
	return Template{}, ErrUnknown
}

// List returns the templates, or an empty list for a nil set so the route
// renders `[]` rather than `null`.
func (s *Set) List() []Template {
	if s == nil {
		return []Template{}
	}
	return s.Templates
}

// Input is a submitter's binding of a source to a declared mount.
type Input struct {
	Source string `json:"source"`
	Mount  string `json:"mount"`
}

type Output struct {
	Mount string `json:"mount"`
}

// Spec is the job spec a template resolves to, in the shape of
// docs/spec/job-api.md.
type Spec struct {
	Image             string            `json:"image"`
	Command           []string          `json:"command"`
	Env               map[string]string `json:"env"`
	Inputs            []Input           `json:"inputs"`
	Outputs           []Output          `json:"outputs"`
	MaxRuntimeSeconds int64             `json:"max_runtime_seconds"`
}

// Overrides is what a submit sent alongside `template`. A nil field was not
// sent. Image and Outputs are carried only so that sending them can be
// refused by name.
type Overrides struct {
	SKU               *string
	Providers         []string
	Image             *string
	Command           []string
	Env               map[string]string
	Inputs            []Input
	Outputs           []Output
	MaxRuntimeSeconds *int64
}

// FieldError names the request field a resolution refused, so the API can
// return it as `param`.
type FieldError struct {
	Code  string
	Field string
	Msg   string
}

func (e *FieldError) Error() string { return e.Msg }

// Resolved is a template with a submit's overrides applied: what the API's
// ordinary submit validation then checks as if the customer had written it.
type Resolved struct {
	SKU       string
	Providers []string
	Spec      Spec
}

// Resolve applies o to t. It refuses an override of a field the template does
// not list, an input on an undeclared mount, and a declared input left
// without a source. It does not check the SKU, providers or runtime: those go
// through the same validation as a hand-written spec.
func (t Template) Resolve(o Overrides) (Resolved, error) {
	allowed := map[string]bool{}
	for _, f := range t.Overridable {
		allowed[f] = true
	}
	sent := map[string]bool{
		FieldSKU:               o.SKU != nil,
		FieldProviders:         o.Providers != nil,
		"spec.image":           o.Image != nil,
		FieldCommand:           o.Command != nil,
		FieldEnv:               o.Env != nil,
		"spec.outputs":         o.Outputs != nil,
		FieldMaxRuntimeSeconds: o.MaxRuntimeSeconds != nil,
	}
	for _, f := range []string{FieldSKU, FieldProviders, "spec.image", FieldCommand, FieldEnv, "spec.outputs", FieldMaxRuntimeSeconds} {
		if sent[f] && !allowed[f] {
			return Resolved{}, &FieldError{Code: "field_not_overridable", Field: f,
				Msg: fmt.Sprintf("template %q does not allow %s to be overridden; it allows %v", t.ID, f, t.Overridable)}
		}
	}

	r := Resolved{SKU: t.SKU, Spec: Spec{
		Image:             t.Image,
		Command:           append([]string(nil), t.Command...),
		Env:               map[string]string{},
		MaxRuntimeSeconds: t.MaxRuntimeSeconds,
	}}
	for k, v := range t.Env {
		r.Spec.Env[k] = v
	}
	for _, m := range t.Outputs {
		r.Spec.Outputs = append(r.Spec.Outputs, Output{Mount: m.Mount})
	}
	if o.SKU != nil {
		r.SKU = *o.SKU
	}
	r.Providers = o.Providers
	if o.Command != nil {
		r.Spec.Command = o.Command
	}
	for k, v := range o.Env {
		if !envKeyRe.MatchString(k) {
			return Resolved{}, &FieldError{Code: "invalid_env", Field: FieldEnv, Msg: fmt.Sprintf("env key %q is not a shell identifier", k)}
		}
		r.Spec.Env[k] = v
	}
	if o.MaxRuntimeSeconds != nil {
		r.Spec.MaxRuntimeSeconds = *o.MaxRuntimeSeconds
	}

	declared := map[string]bool{}
	for _, m := range t.Inputs {
		declared[m.Mount] = true
	}
	bound := map[string]bool{}
	for _, in := range o.Inputs {
		switch {
		case !declared[in.Mount]:
			return Resolved{}, &FieldError{Code: "unknown_input_mount", Field: "spec.inputs",
				Msg: fmt.Sprintf("template %q has no input mount %q", t.ID, in.Mount)}
		case bound[in.Mount]:
			return Resolved{}, &FieldError{Code: "duplicate_input_mount", Field: "spec.inputs",
				Msg: fmt.Sprintf("input mount %q is bound twice", in.Mount)}
		case strings.TrimSpace(in.Source) == "":
			return Resolved{}, &FieldError{Code: "missing_input", Field: "spec.inputs",
				Msg: fmt.Sprintf("input mount %q has no source", in.Mount)}
		}
		bound[in.Mount] = true
	}
	for _, m := range t.Inputs {
		if !bound[m.Mount] {
			return Resolved{}, &FieldError{Code: "missing_input", Field: "spec.inputs",
				Msg: fmt.Sprintf("template %q needs a source for input mount %q (%s)", t.ID, m.Mount, m.Description)}
		}
	}
	r.Spec.Inputs = append(r.Spec.Inputs, o.Inputs...)
	return r, nil
}
