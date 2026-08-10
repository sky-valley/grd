package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
)

const maxCommandInputBytes = 64 * 1024

func readJSONInput[T any](stdin io.Reader, path string) (T, error) {
	var zero T
	reader := stdin
	closeInput := func() error { return nil }
	if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return zero, err
		}
		reader = file
		closeInput = file.Close
	}
	if reader == nil {
		_ = closeInput()
		return zero, errors.New("stdin is unavailable")
	}
	encoded, err := io.ReadAll(io.LimitReader(reader, maxCommandInputBytes+1))
	closeErr := closeInput()
	if err != nil || closeErr != nil {
		return zero, errors.Join(err, closeErr)
	}
	if len(encoded) > maxCommandInputBytes {
		return zero, errors.New("input exceeds 64 KiB")
	}
	var value T
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return zero, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return zero, errors.New("input must contain one JSON value")
	}
	return value, nil
}
