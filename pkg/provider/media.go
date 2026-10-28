package provider

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	_ "image/gif" // decoders for image.Decode
	"image/jpeg"
	"image/png"
	"math"
	"slices"
	"sync"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// Limits are what one vendor's API takes of the images in one request. Each
// envelope sets its own from what the API was seen to reject. Where a limit
// tightens with the number of images, the envelope takes the tight one at any
// number, so a new image never changes the ones sent before it. Bytes and
// Request count raw image bytes; JSON bodies carry base64, 4/3 larger.
type Limits struct {
	Side    int // pixels on the longer side
	Pixels  int // width times height
	Bytes   int // one image
	Count   int // images in one request
	Request int // all images in one request
	// Bound is set where the model signs its thinking over the request before
	// it, as Claude Opus 5.5 does: thinking after an image fit changed would be
	// rejected, so it is not replayed.
	Bound bool
}

// jpegQuality is for an image that is a JPEG or WebP, or still over Bytes as
// PNG: screenshots stay legible at 85.
const jpegQuality = 85

// shrink is each further scale of an image still over Bytes as a JPEG.
const shrink = 0.75

// fittedMax bounds the cache of fitted images. A long session sends the same
// images every step; past this many the cache starts over.
const fittedMax = 256

type fitKey struct {
	id     string
	n      int
	limits Limits
}

// fitted is one image as sent. body is nil when the image goes as stored.
type fitted struct {
	body   []byte
	typ    string
	w, h   int // as sent
	ow, oh int // as stored
	size   int
	err    error
}

var fitCache = struct {
	sync.Mutex
	m map[fitKey]fitted
}{m: map[fitKey]fitted{}}

// fit holds the images of rest under l, so an image the harness took never
// makes a later request invalid. Records keep their bytes; the request is a
// pure function of them, as Compile must be. An image over a side, pixel or
// byte limit is scaled and re-encoded, and the model is told the size it
// sees. An image that does not decode is sent as text saying so. Past Count
// or Request the oldest images become text: when the next image would not
// fit, images go until it fits in half the budget, so the cached prefix
// changes about once per Count/2 images, not every step. Where l.Bound,
// replayed thinking after the first image fit changed is dropped, since the
// image it was signed over may have gone as stored.
func fit(rest []statefile.Record, l Limits) []statefile.Record {
	var idx []int
	for i, r := range rest {
		if slices.Contains(images, r.Type) {
			idx = append(idx, i)
		}
	}
	if len(idx) == 0 {
		return rest
	}
	fits := map[int]fitted{}
	for _, i := range idx {
		fits[i] = fitOne(rest[i], l)
	}
	evicted := map[int]bool{}
	var window []int
	sum := 0
	for _, i := range idx {
		f := fits[i]
		if f.err != nil {
			continue
		}
		if len(window)+1 > l.Count || sum+f.size > l.Request {
			for len(window) > 0 && (len(window)+1 > l.Count/2 || sum+f.size > l.Request/2) {
				evicted[window[0]] = true
				sum -= fits[window[0]].size
				window = window[1:]
			}
		}
		window = append(window, i)
		sum += f.size
	}
	out := make([]statefile.Record, 0, len(rest)+len(idx))
	changed := false
	for i, r := range rest {
		f, ok := fits[i]
		changed = changed || ok && (f.err != nil || evicted[i] || f.body != nil)
		switch {
		case r.Kind == statefile.Thinking && changed && l.Bound:
			continue
		case !ok:
		case f.err != nil:
			r.Type, r.Body = "text/plain", fmt.Appendf(nil, "[image not sent: it does not decode as %s: %v]", r.Type, f.err)
		case evicted[i]:
			r.Type, r.Body = "text/plain", fmt.Appendf(nil, "[image omitted to fit the provider's limits: %dx%d %s, %d KB. Read the file again to see it.]", f.ow, f.oh, r.Type, len(r.Body)/1000)
		case f.body != nil:
			r.Type, r.Body = f.typ, f.body
			note := fmt.Sprintf("shown at %dx%d, file is %dx%d", f.w, f.h, f.ow, f.oh)
			if r.Kind == statefile.Result {
				r.Note = noted(note, r.Note)
			} else { // only results render a note: the note goes as text after the image
				out = append(out, r)
				r = statefile.Record{Header: statefile.Header{Kind: statefile.System, ID: r.ID, Type: "text/plain"}, Body: []byte(note)}
			}
		}
		out = append(out, r)
	}
	return out
}

// fitOne is r's image held under l, from the cache when it has it.
func fitOne(r statefile.Record, l Limits) fitted {
	k := fitKey{r.ID, len(r.Body), l}
	fitCache.Lock()
	f, ok := fitCache.m[k]
	fitCache.Unlock()
	if ok {
		return f
	}
	f = fitImage(r.Body, l)
	fitCache.Lock()
	if len(fitCache.m) >= fittedMax {
		clear(fitCache.m)
	}
	fitCache.m[k] = f
	fitCache.Unlock()
	return f
}

// fitImage scales b to fit l.Side and l.Pixels with CatmullRom, then encodes
// it: PNG and GIF as PNG, JPEG and WebP as JPEG, and as JPEG at shrinking
// scales while over l.Bytes. Same bytes in, same bytes out.
func fitImage(b []byte, l Limits) fitted {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return fitted{err: err}
	}
	f := fitted{w: cfg.Width, h: cfg.Height, ow: cfg.Width, oh: cfg.Height, size: len(b)}
	s := min(1, float64(l.Side)/float64(max(f.ow, f.oh)), math.Sqrt(float64(l.Pixels)/float64(f.ow*f.oh)))
	if s == 1 && len(b) <= l.Bytes {
		return f
	}
	src, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return fitted{err: err}
	}
	asPNG := format == "png" || format == "gif"
	for {
		f.w, f.h = max(1, int(math.Round(float64(f.ow)*s))), max(1, int(math.Round(float64(f.oh)*s)))
		img := image.NewRGBA(image.Rect(0, 0, f.w, f.h))
		if !asPNG { // JPEG has no alpha: transparent pixels go white
			draw.Draw(img, img.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
		}
		draw.CatmullRom.Scale(img, img.Bounds(), src, src.Bounds(), draw.Over, nil)
		var buf bytes.Buffer
		if asPNG {
			err, f.typ = png.Encode(&buf, img), "image/png"
		} else {
			err, f.typ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality}), "image/jpeg"
		}
		if err != nil {
			return fitted{err: err}
		}
		if buf.Len() <= l.Bytes {
			f.body, f.size = buf.Bytes(), buf.Len()
			return f
		}
		if asPNG {
			asPNG = false
		} else {
			s *= shrink
		}
	}
}
