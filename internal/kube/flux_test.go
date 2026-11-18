package kube

import "testing"

func TestNormalizeGitURL(t *testing.T) {
	want := "github.com/acme/fleet"
	for _, u := range []string{
		"git@github.com:acme/fleet.git",
		"ssh://git@github.com/acme/fleet",
		"ssh://git@github.com:22/acme/fleet.git",
		"https://github.com/acme/fleet.git",
		"https://user@github.com/Acme/Fleet/",
	} {
		if got := NormalizeGitURL(u); got != want {
			t.Errorf("NormalizeGitURL(%q) = %q, want %q", u, got, want)
		}
	}
}

func TestHasRevision(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	cases := map[string]bool{
		"main@sha1:" + sha: true,
		"sha1:" + sha:      true,
		"main/" + sha:      true,
		sha:                true,
		"main@sha1:fff":    false,
		"":                 false,
	}
	for rev, want := range cases {
		if got := HasRevision(rev, sha); got != want {
			t.Errorf("HasRevision(%q) = %v, want %v", rev, got, want)
		}
	}
}

func TestParseFluxItemsSourceRef(t *testing.T) {
	items, err := ParseFluxItems([]byte(`{"items":[
	 {"kind":"Kustomization","metadata":{"name":"apps","namespace":"flux-system"},
	  "spec":{"sourceRef":{"kind":"GitRepository","name":"fleet"}},
	  "status":{"lastAppliedRevision":"main@sha1:abc","conditions":[{"type":"Ready","status":"True"}]}},
	 {"kind":"HelmRelease","metadata":{"name":"api","namespace":"api"},
	  "status":{"conditions":[{"type":"Ready","status":"False","message":"boom"}]}}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Name != "api" || !items[0].Failing() {
		t.Fatalf("failing item must sort first: %+v", items)
	}
	ks := items[1]
	if ks.SourceNamespace != "flux-system" || ks.SourceName != "fleet" || ks.AppliedRevision != "main@sha1:abc" {
		t.Fatalf("sourceRef/revision not parsed: %+v", ks)
	}
}

func TestAuthExpired(t *testing.T) {
	yes := []string{
		`Unable to connect to the server: failed to refresh token: oauth2: "invalid_request" "Refresh token is invalid or has already been claimed by another client."`,
		"error: You must be logged in to the server (Unauthorized)",
	}
	no := []string{"dial tcp 10.0.0.1:6443: i/o timeout", "timeout"}
	for _, m := range yes {
		if !AuthExpired(errString(m)) {
			t.Errorf("AuthExpired(%q) = false", m)
		}
	}
	for _, m := range no {
		if AuthExpired(errString(m)) {
			t.Errorf("AuthExpired(%q) = true", m)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }
