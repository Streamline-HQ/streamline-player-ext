// Package envelope builds the error body every failing answer carries:
// {"error":{"code","message",...}} with any extra fields inside "error".
package envelope

import (
	"bytes"
	"encoding/json"
)

// Error returns the body for code and message. extra is key, value pairs
// placed after message in the order given; a pair whose key is not a string,
// or a trailing key with no value, is dropped.
func Error(code, message string, extra ...any) []byte {
	var b bytes.Buffer
	b.WriteString(`{"error":{`)
	field(&b, "code", code)
	b.WriteByte(',')
	field(&b, "message", message)
	for i := 0; i+1 < len(extra); i += 2 {
		key, ok := extra[i].(string)
		if !ok {
			continue
		}
		b.WriteByte(',')
		field(&b, key, extra[i+1])
	}
	b.WriteString(`}}`)
	return b.Bytes()
}

func field(b *bytes.Buffer, key string, value any) {
	k, _ := json.Marshal(key)
	v, err := json.Marshal(value)
	if err != nil {
		v = []byte("null")
	}
	b.Write(k)
	b.WriteByte(':')
	b.Write(v)
}
