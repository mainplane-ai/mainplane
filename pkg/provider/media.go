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
// envelope sets its own from what the API was seen to reject, or smaller where
// the model scales an image down to that itself. Where a limit
// tightens with the number of images, the envelope takes the tight one at any
// number, so a new image never changes the ones sent before it. Bytes and
// Request count raw image bytes; JSON bodies carry base64, 4/3 larger.
type Limits struct {
	Side    int // pixels on the longer side
	Pixels  int // width times height
	Bytes   int // one image
	Count   int // images in one request
	Request int // all images in one request
}

// jpegQuality is for an image that is a JPEG or WebP, or still over Bytes as
// PNG: screenshots stay legible at 85.
const jpegQuality = 85

// shrink is each further scale of an image still over Bytes as a JPEG.
const shrink = 0.75

// The cache of fitted images starts over past fittedMax bytes of scaled
// images or fittedCount entries. A long session sends the same images every
// step, most of them unscaled and nearly free to hold.
const (
	fittedMax   = 256 << 20
	fittedCount = 4096
)

// maxDecode bounds the pixels of an image fit decodes, 4 bytes each: a small
// compressed file can claim any size. A larger image is sent as text.
const maxDecode = 64_000_000

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
	m     map[fitKey]fitted
	bytes int
}{m: map[fitKey]fitted{}}

// fit holds the images of rest under l, so an image the harness took never
// makes a later request invalid, and never changes an image sent before it.
// Records keep their bytes; each image is fit from its own record alone, so
// the request is a pure function of the records and grows only at its end.
// An image over a side, pixel or byte limit is scaled and re-encoded, and
// the model is told the size it sees. An image that does not decode goes as
// text saying so. Once the next image would take the request past l.Count
// or l.Request, it and every image after it go as text: the session's images
// are full, and a new session takes more.
func fit(name string, rest []statefile.Record, l Limits) []statefile.Record {
	out := make([]statefile.Record, 0, len(rest))
	count, sum, full := 0, 0, false
	for _, r := range rest {
		if !slices.Contains(images, r.Type) {
			out = append(out, r)
			continue
		}
		var f fitted
		if !full {
			f = fitOne(r, l)
			full = f.err == nil && (count+1 > l.Count || sum+f.size > l.Request)
		}
		switch {
		case full:
			r.Type, r.Body = "text/plain", fmt.Appendf(nil, "[image not sent: this session is at %s's image limit of %d images or %d MB per request. Continue in a new session to see more images.]", name, l.Count, l.Request/1_000_000)
		case f.err != nil:
			r.Type, r.Body = "text/plain", fmt.Appendf(nil, "[image not sent: %s: %v]", r.Type, f.err)
		case f.body != nil:
			count, sum = count+1, sum+f.size
			r.Type, r.Body = f.typ, f.body
			note := fmt.Sprintf("shown at %dx%d, file is %dx%d", f.w, f.h, f.ow, f.oh)
			if r.Kind == statefile.Result {
				r.Note = noted(note, r.Note)
			} else { // only results render a note: the note goes as text after the image
				out = append(out, r)
				r = statefile.Record{Header: statefile.Header{Kind: statefile.System, ID: r.ID, Type: "text/plain"}, Body: []byte(note)}
			}
		default:
			count, sum = count+1, sum+f.size
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
	if fitCache.bytes+len(f.body) > fittedMax || len(fitCache.m) >= fittedCount {
		clear(fitCache.m)
		fitCache.bytes = 0
	}
	fitCache.m[k] = f
	fitCache.bytes += len(f.body)
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
	if f.ow*f.oh > maxDecode {
		return fitted{err: fmt.Errorf("%dx%d is over the %d pixels the harness decodes", f.ow, f.oh, maxDecode)}
	}
	src, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return fitted{err: err}
	}
	asPNG := format == "png" || format == "gif"
	for {
		f.w, f.h = max(1, int(math.Round(float64(f.ow)*s))), max(1, int(math.Round(float64(f.oh)*s)))
		if max(f.w, f.h) > l.Side || f.w*f.h > l.Pixels { // rounding up went over: floor stays under
			f.w, f.h = max(1, int(float64(f.ow)*s)), max(1, int(float64(f.oh)*s))
		}
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
