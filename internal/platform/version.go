package platform

import (
	"strconv"
	"strings"
)

func parseVersion(value string) ([3]int, bool) {
	var parts [3]int
	fields := strings.Split(strings.TrimPrefix(value, "v"), ".")
	if len(fields) != 3 {
		return parts, false
	}
	for i, field := range fields {
		n, err := strconv.Atoi(field)
		if err != nil || n < 0 {
			return parts, false
		}
		parts[i] = n
	}
	return parts, true
}

func ValidVersion(value string) bool {
	_, ok := parseVersion(value)
	return ok
}

// OlderThan reports whether version is a valid version below minimum; unparseable versions are not judged.
func OlderThan(version, minimum string) bool {
	v, ok := parseVersion(version)
	m, okMin := parseVersion(minimum)
	if !ok || !okMin {
		return false
	}
	for i := range v {
		if v[i] != m[i] {
			return v[i] < m[i]
		}
	}
	return false
}
