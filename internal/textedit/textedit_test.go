package textedit

import (
	"errors"
	"strings"
	"testing"
)

func TestHashIgnoresLineEndings(t *testing.T) {
	if Hash("a\r\nb\r\n") != Hash("a\nb\n") {
		t.Error("hash must ignore CRLF vs LF")
	}
	if Hash("a") == Hash("b") || len(Hash("a")) != 12 {
		t.Error("unexpected hash")
	}
}

func TestUniformCRLF(t *testing.T) {
	for text, want := range map[string]bool{"a\r\nb\r\n": true, "a\nb\n": false, "a\r\nb\n": false, "": false} {
		if UniformCRLF(text) != want {
			t.Errorf("UniformCRLF(%q) != %v", text, want)
		}
	}
}

func TestReplace(t *testing.T) {
	got, err := Replace("one two one", "two", "2", false)
	if err != nil || got.Text != "one 2 one" || got.Replacements != 1 || got.Matched != "exact" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := Replace("one two one", "one", "1", false); err == nil {
		t.Error("an ambiguous match must be refused")
	}
	got, err = Replace("one two one", "one", "1", true)
	if err != nil || got.Text != "1 two 1" || got.Replacements != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := Replace("abc", "zzz", "y", false); err == nil {
		t.Error("a missing text must be refused")
	}
}

func TestReplaceIgnoringTrailingWhitespace(t *testing.T) {
	text := "title  \nbody\t\nend\n"
	got, err := Replace(text, "title\nbody\nend", "X", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "X\n" || got.Matched != "ignoring trailing whitespace" {
		t.Errorf("%+v", got)
	}
	// Regex metacharacters and "$" in the replacement are literal.
	got, err = Replace("a.b (c)  \nd", "a.b (c)\nd", "$1 \\n", false)
	if err != nil || got.Text != "$1 \\n" {
		t.Errorf("%+v %v", got, err)
	}
}

func TestReplaceLines(t *testing.T) {
	text := "l1\nl2\nl3\nl4\n"
	cases := []struct {
		name       string
		start, end int
		newText    string
		want       string
	}{
		{"replace", 2, 3, "x\ny", "l1\nx\ny\nl4\n"},
		{"delete", 2, 3, "", "l1\nl4\n"},
		{"insert", 2, 1, "new\n", "l1\nnew\nl2\nl3\nl4\n"},
		{"append", 5, 4, "l5", "l1\nl2\nl3\nl4\nl5\n"},
		{"crlf input", 1, 1, "a\r\nb", "a\nb\nl2\nl3\nl4\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ReplaceLines(text, c.start, c.end, c.newText)
			if err != nil {
				t.Fatal(err)
			}
			if got.Text != c.want {
				t.Errorf("got %q, want %q", got.Text, c.want)
			}
			if lines, _ := SplitLines(got.Text); got.TotalLines != len(lines) {
				t.Errorf("TotalLines = %d", got.TotalLines)
			}
		})
	}
	for _, bad := range [][2]int{{0, 1}, {6, 5}, {2, 0}, {2, 5}} {
		if _, err := ReplaceLines(text, bad[0], bad[1], "x"); err == nil {
			t.Errorf("range %v must be refused", bad)
		}
	}
}

func TestReplaceLinesKeepsMissingFinalNewline(t *testing.T) {
	got, err := ReplaceLines("a\nb", 2, 2, "c")
	if err != nil || got.Text != "a\nc" {
		t.Fatalf("%+v %v", got, err)
	}
	got, err = ReplaceLines("", 1, 0, "first")
	if err != nil || got.Text != "first\n" || got.TotalLines != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	if !strings.Contains(got.Preview, "     1| first") {
		t.Errorf("preview = %q", got.Preview)
	}
}

func TestNumbered(t *testing.T) {
	if got := Numbered([]string{"a\r", "b"}, 9); got != "     9| a\n    10| b" {
		t.Errorf("%q", got)
	}
}

func TestValidate(t *testing.T) {
	valid := map[string]string{
		"a.json": `{"a": [1, 2]}`, "a.xml": "<a><b/></a>", "a.drawio": "<mxfile/>", "a.md": "<<< not xml",
	}
	for name, text := range valid {
		if err := Validate(name, text); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	invalid := map[string]string{
		"a.json": `{"a": `, "A.XML": "<a><b></a>", "b.xml": "", "c.xml": "<a>", "a.dio": "plain",
	}
	for name, text := range invalid {
		err := Validate(name, text)
		var typed *Error
		if !errors.As(err, &typed) {
			t.Errorf("%s: expected a textedit.Error, got %v", name, err)
		}
	}
}
