package model

// EmbedFailures accumulates embedding failures so Remember can report them
// as one EmbedPartialError after the save succeeds.
type EmbedFailures struct {
	n     int
	first error
}

func (f *EmbedFailures) Add(err error) {
	if err == nil {
		return
	}
	f.n++
	if f.first == nil {
		f.first = err
	}
}

func (f *EmbedFailures) Merge(o *EmbedFailures) {
	if o == nil {
		return
	}
	f.n += o.n
	if f.first == nil {
		f.first = o.first
	}
}

func (f *EmbedFailures) Err() error {
	if f.n == 0 {
		return nil
	}
	return &EmbedPartialError{Count: f.n, Err: f.first}
}

// AddN records n items that are waiting for an embedding.
func (f *EmbedFailures) AddN(n int, err error) {
	if err == nil || n <= 0 {
		return
	}
	f.n += n
	if f.first == nil {
		f.first = err
	}
}
