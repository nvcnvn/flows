package flows

import "encoding/json"

// Codec controls serialization of inputs/outputs/events.
//
// Default is JSONCodec (stored as jsonb).
//
// Implementations should be deterministic: same value => same bytes.
type Codec interface {
	Marshal(v any) ([]byte, error)
	Unmarshal(data []byte, v any) error
}

type JSONCodec struct{}

func (JSONCodec) Marshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

func (JSONCodec) Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
