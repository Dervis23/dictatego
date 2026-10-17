package dictionary

import "testing"

// Expected values mirror what the reference Python implementation produces.
func TestApply(t *testing.T) {
	pairs := []Pair{{Match: "hello", Replace: "hi"}}
	cases := []struct{ text, want string }{
		{"hello", "hi"},
		{"Hello there", "hi there"},
		{"helloo", "helloo"},       // not a whole match
		{"xhellox", "xhellox"},     // surrounded by word chars
		{"say hello, again", "say hi, again"},
		{"HELLO", "hi"},
		{"héllo", "héllo"},
	}
	for _, c := range cases {
		if got := Apply(c.text, pairs); got != c.want {
			t.Errorf("Apply(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}

func TestApplyEmpty(t *testing.T) {
	if got := Apply("text", nil); got != "text" {
		t.Errorf("no pairs: got %q", got)
	}
	if got := Apply("", []Pair{{Match: "a", Replace: "b"}}); got != "" {
		t.Errorf("empty text: got %q", got)
	}
}

func TestLongestMatchWins(t *testing.T) {
	pairs := []Pair{
		{Match: "new york", Replace: "NYC"},
		{Match: "york", Replace: "YK"},
	}
	if got := Apply("new york city", pairs); got != "NYC city" {
		t.Errorf("got %q, want %q", got, "NYC city")
	}
	if got := Apply("york city", pairs); got != "YK city" {
		t.Errorf("got %q, want %q", got, "YK city")
	}
}

func TestNormalizeCaseOverride(t *testing.T) {
	pairs := []Pair{
		{Match: "API", Replace: "API"},
		{Match: "api", Replace: "appi"},
	}
	got := Normalize(pairs)
	if len(got) != 1 || got[0].Match != "api" || got[0].Replace != "appi" {
		t.Errorf("normalize failed: %+v", got)
	}
}

func TestNormalizeWhitespaceAndEmpty(t *testing.T) {
	pairs := []Pair{
		{Match: "  new   line  ", Replace: "next"},
		{Match: "empty", Replace: " "},
		{Match: "", Replace: "x"},
	}
	got := Normalize(pairs)
	if len(got) != 1 || got[0].Match != "new line" || got[0].Replace != "next" {
		t.Errorf("got %+v", got)
	}
}

func TestSentenceCase(t *testing.T) {
	pairs := []Pair{{Match: "im", Replace: "I am"}}
	if got := Apply("Im tired, im on it", pairs); got != "I am tired, I am on it" {
		t.Errorf("got %q", got)
	}
}

func TestUnicodeBoundaries(t *testing.T) {
	pairs := []Pair{{Match: "not", Replace: "doğru"}}
	// Turkish letters count as word characters around the match.
	if got := Apply("not'a bak", pairs); got != "doğru'a bak" {
		t.Errorf("apostrophe boundary is fine: %q", got)
	}
	if got := Apply("xnot", pairs); got != "xnot" {
		t.Errorf("letter boundary blocks: %q", got)
	}
}

func TestSame(t *testing.T) {
	if !Same("API", "api") {
		t.Error("case-only difference should be \"same\"")
	}
	if Same("API", "AP1") {
		t.Error("different strings must not be same")
	}
}
