package pdfrender

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"reflect"
	"testing"

	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/testutil/samples"
)

func TestParsePages(t *testing.T) {
	valid := map[string][]int{
		"": {1}, "1": {1}, " 3 ": {3}, "2-4": {2, 3, 4}, "5,1,3": {1, 3, 5}, "1-2, 2-3": {1, 2, 3}, "1-5": {1, 2, 3, 4, 5},
	}
	for selection, want := range valid {
		got, err := ParsePages(selection, 5)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("ParsePages(%q) = %v, %v; want %v", selection, got, err, want)
		}
	}
	for _, selection := range []string{"0", "-1", "a", "1-", "4-2", "1-6", "1,2,3,4,5,6", "1-999999999", "1;2", "1.5"} {
		if _, err := ParsePages(selection, 5); err == nil {
			t.Errorf("ParsePages(%q) must be refused", selection)
		}
	}
}

func TestRenderPage(t *testing.T) {
	binary := Find()
	if binary == "" {
		t.Skip("pdftoppm is not installed")
	}
	document := samples.PDF("First page", "Second page")
	image, err := Page(context.Background(), binary, document, 2, 50)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(bytes.NewReader(image))
	if err != nil {
		t.Fatal(err)
	}
	// US Letter (612x792 pt) at 50 dpi.
	if bounds := decoded.Bounds(); bounds.Dx() != 425 || bounds.Dy() != 550 {
		t.Errorf("size = %v", bounds)
	}
	if _, err := Page(context.Background(), binary, document, 9, 50); !errors.Is(err, ErrNoSuchPage) {
		t.Errorf("a missing page: %v", err)
	}
	if _, err := Page(context.Background(), binary, []byte("not a pdf"), 1, 50); err == nil {
		t.Error("a broken PDF must be an error")
	}
}
