package ingestion

import "encoding/json"

// jsonUnmarshalImpl is the concrete implementation used by jsonUnmarshal.
func jsonUnmarshalImpl(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}
