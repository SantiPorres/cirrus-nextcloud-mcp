// Package pdfrender turns pages of a PDF into PNG images with the external
// tool pdftoppm (from poppler-utils). The tool is optional: when it is not
// installed, Find reports so and the calling tool is simply not offered.
package pdfrender

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Timeout bounds the rendering of one page.
const Timeout = 60 * time.Second

// Find returns the path of pdftoppm, or "" when it is not installed.
func Find() string {
	path, err := exec.LookPath("pdftoppm")
	if err != nil {
		return ""
	}
	return path
}

// ErrNoSuchPage is returned when the PDF has fewer pages than requested.
var ErrNoSuchPage = errors.New("the PDF does not have that page")

// Page renders one page (1-based) of a PDF at the given resolution and returns a PNG.
// binary is the path returned by Find.
func Page(ctx context.Context, binary string, pdf []byte, page, dpi int) ([]byte, error) {
	dir, err := os.MkdirTemp("", "cirrus-pdf-")
	if err != nil {
		return nil, fmt.Errorf("could not create a temporary folder: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	input := filepath.Join(dir, "in.pdf")
	if err := os.WriteFile(input, pdf, 0o600); err != nil {
		return nil, fmt.Errorf("could not write the temporary file: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()

	// Fixed arguments, no shell: nothing from the request reaches the command line except two integers.
	pageArg := strconv.Itoa(page)
	command := exec.CommandContext(ctx, binary, "-png", "-singlefile", "-r", strconv.Itoa(dpi), "-f", pageArg, "-l", pageArg, input, filepath.Join(dir, "out")) //nolint:gosec // see above
	var stderr bytes.Buffer
	command.Stderr = &stderr
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "TMPDIR=" + dir}
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("rendering the page took too long")
		}
		message := strings.ToLower(stderr.String())
		if strings.Contains(message, "wrong page range") || strings.Contains(message, "first page") {
			return nil, ErrNoSuchPage
		}
		return nil, errors.New("the PDF could not be rendered (it may be damaged or password-protected)")
	}
	output, err := os.ReadFile(filepath.Join(dir, "out.png")) //nolint:gosec // path built above
	if err != nil {
		return nil, ErrNoSuchPage
	}
	return output, nil
}

// ParsePages reads a page selection such as "1", "2-4" or "1,3,5" into a
// sorted list without duplicates, refusing more than limit pages.
func ParsePages(selection string, limit int) ([]int, error) {
	selection = strings.TrimSpace(selection)
	if selection == "" {
		return []int{1}, nil
	}
	seen := map[int]bool{}
	var pages []int
	add := func(page int) error {
		if page < 1 {
			return errors.New("page numbers start at 1")
		}
		if !seen[page] {
			seen[page] = true
			pages = append(pages, page)
		}
		if len(pages) > limit {
			return fmt.Errorf("at most %d pages can be rendered per call", limit)
		}
		return nil
	}
	for _, part := range strings.Split(selection, ",") {
		first, last, isRange := strings.Cut(strings.TrimSpace(part), "-")
		from, err := strconv.Atoi(strings.TrimSpace(first))
		if err != nil {
			return nil, fmt.Errorf("%q is not a page selection (use forms like \"1\", \"2-4\" or \"1,3\")", selection)
		}
		to := from
		if isRange {
			if to, err = strconv.Atoi(strings.TrimSpace(last)); err != nil || to < from {
				return nil, fmt.Errorf("%q is not a page selection (use forms like \"1\", \"2-4\" or \"1,3\")", selection)
			}
		}
		if to-from >= limit {
			return nil, fmt.Errorf("at most %d pages can be rendered per call", limit)
		}
		for page := from; page <= to; page++ {
			if err := add(page); err != nil {
				return nil, err
			}
		}
	}
	for i := 1; i < len(pages); i++ {
		for j := i; j > 0 && pages[j] < pages[j-1]; j-- {
			pages[j], pages[j-1] = pages[j-1], pages[j]
		}
	}
	return pages, nil
}
