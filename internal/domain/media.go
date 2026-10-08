package domain

import (
	"net/url"
	"strings"
)

// ValidImageURL is the format boundary for catalog data. Network address checks
// are additionally required when opening a connection or following a redirect.
func ValidImageURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && strings.EqualFold(u.Hostname(), "i.pximg.net") && (u.Port() == "" || u.Port() == "443") && u.User == nil && u.Fragment == "" && strings.HasPrefix(u.Path, "/")
}
