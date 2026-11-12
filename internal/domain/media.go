package domain

import (
	"net/url"
	"strings"
)

// ValidAvatarURL also accepts Pixiv's static default avatar. It does not expand
// the artwork/CDN target allowlist used by selection or preview forwarding.
func ValidAvatarURL(raw string) bool {
	if ValidImageURL(raw) {
		return true
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && strings.EqualFold(u.Hostname(), "s.pximg.net") &&
		(u.Port() == "" || u.Port() == "443") && u.User == nil && u.Fragment == "" &&
		u.EscapedPath() == "/common/images/no_profile.png" && u.RawQuery == ""
}

// ValidImageURL is the format boundary for catalog data. Network address checks
// are additionally required when opening a connection or following a redirect.
func ValidImageURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && strings.EqualFold(u.Hostname(), "i.pximg.net") && (u.Port() == "" || u.Port() == "443") && u.User == nil && u.Fragment == "" && strings.HasPrefix(u.Path, "/")
}
