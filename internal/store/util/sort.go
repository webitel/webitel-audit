package util

import (
	"fmt"
	"strings"

	"github.com/webitel/webitel-go-kit/pkg/errors"
)

func ParseSort(sort string, sortable map[string]string) ([]string, error) {
	criteria := strings.Split(sort, ",")
	orderBy := make([]string, 0, len(criteria))

	for _, criterion := range criteria {
		criterion = strings.TrimSpace(criterion)
		if criterion == "" {
			continue
		}

		direction := "ASC"

		switch criterion[0] {
		case '-':
			direction = "DESC"
			criterion = criterion[1:]
		case '+':
			criterion = criterion[1:]
		}

		name := strings.ToLower(criterion)

		expr, ok := sortable[name]
		if !ok {
			return nil, unknownField("sort", criterion)
		}

		orderBy = append(orderBy, expr+" "+direction)
	}

	return orderBy, nil
}

func unknownField(kind, name string) error {
	return errors.InvalidArgument(fmt.Sprintf("%s: unknown field %q", kind, name),
		errors.WithID("store.util."+kind), errors.WithValue("field", name))
}
