package domain

import "testing"

func TestDefaultAvatarDoesNotExpandArtworkURLAllowlist(t *testing.T) {
	const fallback = "https://s.pximg.net/common/images/no_profile.png"
	if !ValidAvatarURL(fallback) {
		t.Fatal("official default avatar was rejected")
	}
	if ValidImageURL(fallback) {
		t.Fatal("default avatar must not qualify as artwork media")
	}
	for _, raw := range []string{
		"http://s.pximg.net/common/images/no_profile.png",
		"https://s.pximg.net.evil.test/common/images/no_profile.png",
		"https://user:pass@s.pximg.net/common/images/no_profile.png",
		"https://s.pximg.net:444/common/images/no_profile.png",
		"https://s.pximg.net/common/images/no_profile.png#fragment",
		"https://s.pximg.net/common/images/no_profile.png?url=https://evil.test",
		"https://s.pximg.net/other.png",
	} {
		if ValidAvatarURL(raw) {
			t.Errorf("unsafe or unsupported avatar URL accepted: %q", raw)
		}
	}
}
