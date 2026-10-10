package chattitle

import "testing"

func TestDeriveSkipsImageOnlyLines(t *testing.T) {
	cases := map[string]string{
		"![image.png](https://x/a)\n\n对方分享给我了，你先帮我同意": "对方分享给我了，你先帮我同意",
		"![image.png](https://x/a)":     "image.png",
		"看这张 ![image.png](https://x/a)": "看这张 image.png",
		"first line\nsecond":            "first line",
	}
	for in, want := range cases {
		if got := Derive(in); got != want {
			t.Errorf("Derive(%q) = %q, want %q", in, got, want)
		}
	}
}
