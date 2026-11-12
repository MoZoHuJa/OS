// Package modelmeta — yaml_strict.go: the STRICT single-document YAML
// decode (v19.8.1, KPK-19.8.0-P3-06).
//
// The v19.7.0–v19.8.0 loaders used yaml.Unmarshal, whose default IGNORES
// unknown mapping keys: a misspelled required field usually still failed
// (the real field stayed empty), but an EXTRA unsupported field — or a
// typo in a semantically adjacent optional field — was silently
// accepted. For OPERATOR-CONTROLLED manifests (model manifests, InferSSD
// runtime profiles, SGLang Expert Pack manifests) a silently-accepted
// unknown field is a configuration the system never agreed to carry.
//
// The rule this helper enforces, for every operator-controlled YAML in
// the repo:
//
//   - unknown fields (top-level OR nested) are a NAMED parse error —
//     yaml.v3 KnownFields(true);
//   - MULTIPLE YAML documents in one file are refused (a manifest is
//     one document; a second one is not the loader's to choose between);
//   - the error message carries the same "YAML parse failed:" prefix the
//     loaders already emit, so existing invalid-entry reporting keeps
//     its shape.
package modelmeta

import (
	"bytes"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// DecodeSingleStrictYAML decodes EXACTLY ONE YAML document into out,
// refusing unknown fields and any trailing second document. It returns
// a nil error only when the whole input is one schema-known document.
func DecodeSingleStrictYAML(raw []byte, out interface{}) error {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		if err == io.EOF {
			return fmt.Errorf("the document is empty — one YAML document is required, zero is none")
		}
		return err
	}
	// A second document must be refused BY NAME, not silently dropped.
	var extra interface{}
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple YAML documents — exactly one document is loaded, the rest are refused (never silently chosen between)")
		}
		return fmt.Errorf("trailing content after the first YAML document: %v", err)
	}
	return nil
}
