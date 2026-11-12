package statefile

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// Read returns every whole record in a file. A torn final record is dropped.
func Read(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	recs, _, err := scan(f)
	_ = f.Close()
	return recs, err
}

// scan reads records until EOF or a torn record and returns them with the
// byte offset where the first unreadable record begins.
func scan(f *os.File) ([]Record, int64, error) {
	br := bufio.NewReader(f)
	var recs []Record
	var good int64
	for {
		r, err := Decode(br)
		if errors.Is(err, io.EOF) || errors.Is(err, ErrTorn) {
			return recs, good, nil
		}
		if err != nil {
			return nil, 0, err
		}
		recs = append(recs, r)
		pos, _ := f.Seek(0, io.SeekCurrent)
		good = pos - int64(br.Buffered())
	}
}

// File is a state file open for appending. One writer per file.
type File struct {
	Path string
	f    *os.File
	n    int
}

// Create makes a new file whose first two records are start and config. The
// start record says who made the session; no model request holds it.
func Create(path, via string, config []byte) (*File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	sf := &File{Path: path, f: f}
	if err := sf.Append(Record{Header: Header{Kind: Start, Via: via}}); err != nil {
		return nil, err
	}
	return sf, sf.Append(Record{Header: Header{Kind: Config, Type: "application/json"}, Body: config})
}

// Copy makes dst from records 1 through n-1 of src: the start record with
// via, who made the copy, and the rest byte for byte. The prefix of an
// append-only file never changes, so the copy is the same session under a
// new name, and its maker's.
func Copy(src, dst, via string, n int) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	br := bufio.NewReader(in)
	at := func() int64 {
		pos, _ := in.Seek(0, io.SeekCurrent)
		return pos - int64(br.Buffered())
	}
	var start []byte
	var rest int64
	for i := 1; i < n; i++ {
		r, err := Decode(br)
		if err != nil {
			return fmt.Errorf("record %d: %w", i, err)
		}
		if i == 1 {
			r.Via = via
			if start, err = Encode(r); err != nil {
				return err
			}
			rest = at()
		}
	}
	end := at()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err = out.Write(start); err == nil {
		if _, err = in.Seek(rest, io.SeekStart); err == nil {
			_, err = io.CopyN(out, in, end-rest)
		}
	}
	if err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

// Open opens an existing file for appending. A torn final record is cut off.
func Open(path string) (*File, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	recs, good, err := scan(f)
	if err != nil {
		return nil, err
	}
	if err := f.Truncate(good); err != nil {
		return nil, err
	}
	if _, err := f.Seek(good, io.SeekStart); err != nil {
		return nil, err
	}
	return &File{Path: path, f: f, n: len(recs)}, nil
}

// Append writes one record and fills in n, id, time, and len.
func (sf *File) Append(r Record) error {
	r.N = sf.n + 1
	if r.ID == "" {
		r.ID = NewID()
	}
	r.Time = time.Now().UTC().Truncate(time.Millisecond)
	b, err := Encode(r)
	if err != nil {
		return err
	}
	if _, err := sf.f.Write(b); err != nil {
		return err
	}
	sf.n = r.N
	return nil
}

// N is the index of the last record written.
func (sf *File) N() int { return sf.n }

func (sf *File) Close() error { return sf.f.Close() }
