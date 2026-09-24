package discovery

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

// jsonValue is one parsed JSON value: *jsonObject, []jsonValue, string,
// json.Number, bool, or nil. Objects keep their key order for stable discovery output.
type jsonValue = any

// jsonObject is an insertion-ordered JSON object.
type jsonObject struct {
	keys   []string
	values map[string]jsonValue
}

func newJSONObject() *jsonObject { return &jsonObject{values: map[string]jsonValue{}} }

// Get returns a member.
func (o *jsonObject) Get(key string) (jsonValue, bool) {
	value, ok := o.values[key]
	return value, ok
}

// Set adds or replaces a member, keeping an existing key's position.
func (o *jsonObject) Set(key string, value jsonValue) {
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

// Keys returns the member names in order.
func (o *jsonObject) Keys() []string { return append([]string(nil), o.keys...) }

// ObjectIfPresent returns the member as an object without creating it.
func (o *jsonObject) ObjectIfPresent(key string) (*jsonObject, bool) {
	child, ok := o.values[key].(*jsonObject)
	return child, ok
}

// Array returns the member as an array (nil when absent or another type).
func (o *jsonObject) Array(key string) ([]jsonValue, bool) {
	array, ok := o.values[key].([]jsonValue)
	return array, ok
}

// String returns a string member.
func (o *jsonObject) String(key string) string {
	value, _ := o.values[key].(string)
	return value
}

// parseJSON decodes document into an ordered value tree.
func parseJSON(document []byte) (jsonValue, error) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	value, err := parseJSONValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("trailing data after the JSON document")
	}
	return value, nil
}

func parseJSONValue(decoder *json.Decoder) (jsonValue, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	return parseJSONFrom(decoder, token)
}

func parseJSONFrom(decoder *json.Decoder, token json.Token) (jsonValue, error) {
	switch t := token.(type) {
	case json.Delim:
		switch t {
		case '{':
			object := newJSONObject()
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, fmt.Errorf("object key %v is not a string", keyToken)
				}
				value, err := parseJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				object.Set(key, value)
			}
			if _, err := decoder.Token(); err != nil { // consume '}'
				return nil, err
			}
			return object, nil
		case '[':
			array := []jsonValue{}
			for decoder.More() {
				value, err := parseJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				array = append(array, value)
			}
			if _, err := decoder.Token(); err != nil { // consume ']'
				return nil, err
			}
			return array, nil
		}
		return nil, fmt.Errorf("unexpected delimiter %v", t)
	default:
		return t, nil // string, json.Number, bool, nil
	}
}

// jsonDocument is the parsed object used by read-only discovery.
type jsonDocument struct {
	Root *jsonObject
}

// errNotStrictJSON marks a file that exists but is not strict JSON (a
// JSON5 OpenClaw config, say).
var errNotStrictJSON = errors.New("file is not strict JSON")

// loadJSONObject reads a JSON file whose root is an object. A missing file
// yields an empty document.
func loadJSONObject(content []byte, existed bool) (*jsonDocument, error) {
	doc := &jsonDocument{Root: newJSONObject()}
	if !existed || len(bytes.TrimSpace(content)) == 0 {
		return doc, nil
	}
	value, err := parseJSON(content)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNotStrictJSON, err)
	}
	root, ok := value.(*jsonObject)
	if !ok {
		return nil, fmt.Errorf("%w: root is not an object", errNotStrictJSON)
	}
	doc.Root = root
	return doc, nil
}

// readFile returns a file's content and whether it existed.
func readFile(path string) ([]byte, bool, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return content, true, nil
}
