// Package result defines the structured, machine-readable result and error
// types shared by every layer of Focal (vo, policy, ssh, config). It has no
// dependency outside the standard library.
package result

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Kind classifies which layer rejected or failed an operation.
type Kind string

const (
	// KindValidation marks input rejected by a value object before it
	// reaches policy or execution (e.g. an invalid Target or ServiceName).
	KindValidation Kind = "validation"
	// KindPolicy marks input rejected by administrator/operation policy
	// (e.g. a sudo mode the operation's capability does not allow).
	KindPolicy Kind = "policy"
	// KindExecution marks a failure that occurred while running a command
	// over SSH (e.g. connection failure, non-zero exit, timeout).
	KindExecution Kind = "execution"
)

// Error is the fixed JSON schema returned for every error in Focal,
// regardless of which layer produced it. Field order is fixed and mirrors
// the JSON key order produced by Encode.
type Error struct {
	Kind    Kind     `json:"kind"`
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Field   string   `json:"field,omitempty"`
	Allowed []string `json:"allowed,omitempty"`
}

// Error implements the error interface.
func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s: %s", e.Kind, e.Code, e.Message)
}

// ValidationError builds a Kind: validation Error. field and allowed
// describe which input was rejected and what would have been accepted;
// either may be left zero-valued when not applicable.
func ValidationError(code, message, field string, allowed []string) *Error {
	return &Error{Kind: KindValidation, Code: code, Message: message, Field: field, Allowed: allowed}
}

// PolicyError builds a Kind: policy Error, produced when administrator
// policy and operation capability intersect to reject a request.
func PolicyError(code, message, field string, allowed []string) *Error {
	return &Error{Kind: KindPolicy, Code: code, Message: message, Field: field, Allowed: allowed}
}

// ExecutionError builds a Kind: execution Error, produced when running a
// command over SSH fails. Execution errors have no field/allowed set.
func ExecutionError(code, message string) *Error {
	return &Error{Kind: KindExecution, Code: code, Message: message}
}

// Encode marshals err as compact, single-line JSON with HTML escaping
// disabled, so tokens like <, >, and & used in target/host values are not
// rewritten.
func Encode(err *Error) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if encErr := enc.Encode(err); encErr != nil {
		return nil, encErr
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
