package localvalidation

import "encoding/json"

// jsonMarshal centralizes compact stable list encoding for command output.
func jsonMarshal(value any) ([]byte, error) { return json.Marshal(value) }
