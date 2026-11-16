package util

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// PlainPropertiesMarshaler renders a config map as sorted plain "key=value"
// lines with no Java-properties escaping — the exact output of operator-go
// v0.12.6 pkg/config/properties.Properties.Marshal, which the Gen 2 rendered
// nifi.properties/bootstrap.conf bytes (and the gomplate templates inside
// them) depend on. The framework's default PropertiesAdapter escapes spaces
// and colons, which would rewrite every value of the parity contract.
type PlainPropertiesMarshaler struct{}

func (PlainPropertiesMarshaler) Marshal(data map[string]string) (string, error) {
	var sb strings.Builder
	for _, key := range slices.Sorted(maps.Keys(data)) {
		if _, err := fmt.Fprintf(&sb, "%s=%s\n", key, data[key]); err != nil {
			return "", err
		}
	}
	return sb.String(), nil
}
