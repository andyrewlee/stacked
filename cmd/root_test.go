package cmd

import "testing"

// TestResolveVersion pins the version-resolution ladder: ldflags wins; a real
// module version beats the compiled-in default; "(devel)"/empty keep the
// default.
func TestResolveVersion(t *testing.T) {
	tests := []struct {
		name    string
		ldflags string
		main    string
		want    string
	}{
		{name: "ldflags stamped wins", ldflags: "abc1234-dirty", main: "v0.2.0", want: "abc1234-dirty"},
		{name: "module version beats default", ldflags: defaultVersion, main: "v0.2.0", want: "v0.2.0"},
		{name: "devel keeps default", ldflags: defaultVersion, main: "(devel)", want: defaultVersion},
		{name: "empty keeps default", ldflags: defaultVersion, main: "", want: defaultVersion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveVersion(tt.ldflags, tt.main); got != tt.want {
				t.Fatalf("resolveVersion(%q, %q) = %q, want %q", tt.ldflags, tt.main, got, tt.want)
			}
		})
	}
}
