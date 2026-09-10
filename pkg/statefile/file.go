package statefile

import (
	"bufio"
	"errors"
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
		r, err := decode(br)
		if errors.Is(err, io.EOF) || errors.Is(err, errTorn) {
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

// Create makes a new file whose first two records are first (start or link)
// and config.
func Create(path string, first Record, config []byte) (*File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	sf := &File{Path: path, f: f}
	if err := sf.Append(first); err != nil {
		return nil, err
	}
	return sf, sf.Append(Record{Header: Header{Kind: Config, Type: "application/json"}, Body: config})
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
	b, err := encode(r)
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
