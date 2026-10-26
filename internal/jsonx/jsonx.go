// Package jsonx is the only package in this module allowed to import
// encoding/json. Business code must access JSON through these wrappers so a
// future change of the JSON implementation stays local to this package.
package jsonx

import "encoding/json"

// RawMessage is a raw encoded JSON value.
type RawMessage = json.RawMessage

// Marshal returns the JSON encoding of v.
func Marshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

// Unmarshal parses JSON data and stores the result in v.
func Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
