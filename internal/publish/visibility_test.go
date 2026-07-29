package publish

import "testing"

func TestNormalizeVisibility(t *testing.T) {
	cases := map[string]string{
		"public":    VisibilityPublic,
		"followers": VisibilityFollowers,
		"private":   VisibilityPrivate,
		"":          VisibilityPublic,
		"nonsense":  VisibilityPublic,
		"PUBLIC":    VisibilityPublic,
	}
	for in, want := range cases {
		if got := NormalizeVisibility(in); got != want {
			t.Errorf("NormalizeVisibility(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPostPublicAndFederates(t *testing.T) {
	cases := []struct {
		visibility string
		public     bool
		federates  bool
	}{
		{"", true, true},
		{VisibilityPublic, true, true},
		{VisibilityFollowers, false, true},
		{VisibilityPrivate, false, false},
	}
	for _, c := range cases {
		p := &Post{Visibility: c.visibility}
		if p.Public() != c.public {
			t.Errorf("Public() for %q = %v, want %v", c.visibility, p.Public(), c.public)
		}
		if p.Federates() != c.federates {
			t.Errorf("Federates() for %q = %v, want %v", c.visibility, p.Federates(), c.federates)
		}
	}
}
