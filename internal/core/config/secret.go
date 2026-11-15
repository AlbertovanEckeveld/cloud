package config

import "log/slog"

const redacted = "[REDACTED]"

// Secret holds a sensitive value such as a password or access key.
//
// Every way Go can format or serialize a value (fmt verbs, slog, JSON, text
// marshalling) yields "[REDACTED]", so a secret cannot leak into logs or API
// responses by accident. Call Reveal only at the point where the raw value is
// handed to a client library.
//
// Caveat: fmt cannot call methods on unexported struct fields, so printing a
// struct with %v that holds a Secret in an unexported field prints the raw
// value. Keep Secret fields exported, or never print such structs.
type Secret string

// Reveal returns the raw secret value.
func (s Secret) Reveal() string { return string(s) }

// String implements fmt.Stringer (%s, %v).
func (Secret) String() string { return redacted }

// GoString implements fmt.GoStringer (%#v).
func (Secret) GoString() string { return redacted }

// LogValue implements slog.LogValuer.
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalText implements encoding.TextMarshaler, which encoding/json also uses.
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }
