package harness

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// The apply_patch format GPT families were trained on:
//
//	*** Begin Patch
//	*** Add File: path
//	+line
//	*** Update File: path
//	@@ locator
//	 context
//	-old
//	+new
//	*** End of File
//	*** End Patch
//
// Delete File and Move to are refused before anything is written: the worker
// has no delete primitive and run does the job. opencode deletes and refuses
// moves; codex does both.

type hunk struct {
	locators []string
	old, new []string
	eof      bool
}

type section struct {
	path  string
	add   []string // Add File content
	hunks []hunk   // Update File
}

func parsePatch(input string) ([]section, error) {
	lines := strings.Split(strings.ReplaceAll(input, "\r\n", "\n"), "\n")
	if len(lines) == 0 || lines[0] != "*** Begin Patch" {
		return nil, fmt.Errorf("patch must start with *** Begin Patch")
	}
	var secs []section
	var cur *section
	var h *hunk
	for _, l := range lines[1:] {
		switch {
		case l == "*** End Patch":
			return secs, nil
		case strings.HasPrefix(l, "*** Add File: "):
			secs = append(secs, section{path: strings.TrimPrefix(l, "*** Add File: "), add: []string{}})
			cur, h = &secs[len(secs)-1], nil
		case strings.HasPrefix(l, "*** Update File: "):
			secs = append(secs, section{path: strings.TrimPrefix(l, "*** Update File: ")})
			cur, h = &secs[len(secs)-1], nil
		case strings.HasPrefix(l, "*** Delete File: "), strings.HasPrefix(l, "*** Move to: "):
			return nil, fmt.Errorf("%q: not supported, use run", l)
		case cur == nil:
			return nil, fmt.Errorf("line outside a file section: %q", l)
		case cur.add != nil:
			if !strings.HasPrefix(l, "+") {
				return nil, fmt.Errorf("add file line must start with +: %q", l)
			}
			cur.add = append(cur.add, l[1:])
		case strings.HasPrefix(l, "@@"):
			if h == nil || len(h.old)+len(h.new) > 0 {
				cur.hunks = append(cur.hunks, hunk{})
				h = &cur.hunks[len(cur.hunks)-1]
			}
			if loc := strings.TrimSpace(strings.TrimPrefix(l, "@@")); loc != "" {
				h.locators = append(h.locators, loc)
			}
		case l == "*** End of File":
			if h == nil {
				return nil, fmt.Errorf("*** End of File outside a hunk")
			}
			h.eof = true
		default:
			if h == nil {
				cur.hunks = append(cur.hunks, hunk{})
				h = &cur.hunks[len(cur.hunks)-1]
			}
			switch {
			case strings.HasPrefix(l, "+"):
				h.new = append(h.new, l[1:])
			case strings.HasPrefix(l, "-"):
				h.old = append(h.old, l[1:])
			case strings.HasPrefix(l, " "):
				h.old, h.new = append(h.old, l[1:]), append(h.new, l[1:])
			case l == "":
				h.old, h.new = append(h.old, ""), append(h.new, "")
			default:
				return nil, fmt.Errorf("hunk line must start with +, -, or space: %q", l)
			}
		}
	}
	return nil, fmt.Errorf("patch must end with *** End Patch")
}

// apply rewrites file lines by every hunk in order, each search starting where
// the previous hunk ended.
func apply(lines []string, hunks []hunk) ([]string, error) {
	cursor := 0
	for _, h := range hunks {
		for _, loc := range h.locators {
			i := slices.IndexFunc(lines[cursor:], func(l string) bool { return strings.TrimSpace(l) == loc })
			if i < 0 {
				return nil, fmt.Errorf("locator not found: @@ %s", loc)
			}
			cursor += i + 1
		}
		at := cursor
		switch {
		case h.eof:
			at = len(lines) - len(h.old)
			if at < cursor || !slices.Equal(lines[at:], h.old) {
				return nil, fmt.Errorf("hunk does not match end of file:\n%s", strings.Join(h.old, "\n"))
			}
		case len(h.old) > 0:
			i := indexSeq(lines[cursor:], h.old)
			if i < 0 {
				return nil, fmt.Errorf("hunk does not match file:\n%s", strings.Join(h.old, "\n"))
			}
			at = cursor + i
		}
		lines = slices.Concat(lines[:at], h.new, lines[at+len(h.old):])
		cursor = at + len(h.new)
	}
	return lines, nil
}

func indexSeq(lines, seq []string) int {
	for i := 0; i+len(seq) <= len(lines); i++ {
		if slices.Equal(lines[i:i+len(seq)], seq) {
			return i
		}
	}
	return -1
}

// applyPatch computes every file before writing any, so a hunk that fails to
// match leaves the worker as it was and the model can retry the whole patch.
func applyPatch(ctx context.Context, w Worker, input string) error {
	secs, err := parsePatch(input)
	if err != nil {
		return err
	}
	out := make([][]byte, len(secs))
	for i, s := range secs {
		lines := s.add
		if s.add == nil {
			b, err := w.Read(ctx, s.path)
			if err != nil {
				return err
			}
			if text := strings.ReplaceAll(string(b), "\r\n", "\n"); text != "" {
				lines = strings.Split(strings.TrimSuffix(text, "\n"), "\n")
			}
			if lines, err = apply(lines, s.hunks); err != nil {
				return fmt.Errorf("%s: %w", s.path, err)
			}
		}
		out[i] = []byte(strings.Join(lines, "\n") + "\n")
	}
	for i, s := range secs {
		if err := w.Write(ctx, s.path, out[i]); err != nil {
			return err
		}
	}
	return nil
}
