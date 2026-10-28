package config

import "testing"

func TestLatencyExcludeJSON(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, "[]"},
		{[]string{"/api/demand"}, `["/api/demand"]`},
		{[]string{"/a", `/b"c`}, `["/a","/b\"c"]`},
	}
	for _, c := range cases {
		if got := (Config{LatencyExclude: c.in}).LatencyExcludeJSON(); got != c.want {
			t.Errorf("LatencyExcludeJSON(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

// An empty LATENCY_EXCLUDE must turn the default off, not fall back to it.
func TestLatencyExcludeEnv(t *testing.T) {
	t.Setenv("LATENCY_EXCLUDE", "")
	if got := Load().LatencyExclude; len(got) != 0 {
		t.Errorf("LATENCY_EXCLUDE= gave %q, want none", got)
	}
	t.Setenv("LATENCY_EXCLUDE", "/a, /b")
	if got := Load().LatencyExclude; len(got) != 2 || got[0] != "/a" || got[1] != "/b" {
		t.Errorf("LATENCY_EXCLUDE=/a, /b gave %q", got)
	}
}
