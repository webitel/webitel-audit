package util

import (
	"slices"
	"strings"
	"unicode"
)

func ParseFields(fields, allowed, defaults []string) ([]string, error) {
	selected := make([]string, 0, len(fields))

	for _, field := range fields {
		for _, name := range strings.FieldsFunc(field, isFieldSeparator) {
			name = strings.ToLower(name)

			if !slices.Contains(allowed, name) {
				return nil, unknownField("fields", name)
			}

			if !slices.Contains(selected, name) {
				selected = append(selected, name)
			}
		}
	}

	if len(selected) == 0 {
		return slices.Clone(defaults), nil
	}

	return selected, nil
}

func isFieldSeparator(r rune) bool { return r == ',' || unicode.IsSpace(r) }
