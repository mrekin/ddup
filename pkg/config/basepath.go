package config

import "strings"

// GetBasePath returns the normalized base path: an empty string if unset, otherwise a path with a leading "/" and no trailing slash
func (c ConfigServer) GetBasePath() string {
	p := strings.Trim(strings.TrimSpace(c.BasePath), "/")
	if p == "" {
		return ""
	}
	return "/" + p
}
