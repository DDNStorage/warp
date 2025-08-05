package generator

import (
	"fmt"
	"io"
	"math/rand"
)

// CircularPartialCompressibleOpts are the options for the circular partially compressible data source.
type CircularPartialCompressibleOpts struct {
	seed                  *int64
	size                  int
	percentCompressible   int
	compressibleChunkSize int
}

func WithPartialCompressability(percent int) CircularPartialCompressibleOpts {
	return CircularPartialCompressibleOpts{
		seed: nil,
		// Use 2^20 + 1 (1MB + 1 byte) as default size
		size:                  1<<20 + 1,
		percentCompressible:   percent,
		compressibleChunkSize: 32000,
	}
}

// Apply Circular Random data options.
func (o CircularPartialCompressibleOpts) Apply() Option {
	return func(opts *Options) error {
		if err := o.validate(); err != nil {
			return err
		}
		opts.partialCompr = o
		opts.src = newCircularPartialCompressible
		return nil
	}
}

func (o CircularPartialCompressibleOpts) validate() error {
	if o.size <= 0 {
		return fmt.Errorf("circular random: size must be > 0, got %d", o.size)
	}
	if o.size <= o.compressibleChunkSize {
		return fmt.Errorf("circular random: size must be > compressibleChunkSize, got %d and %d", o.size, o.compressibleChunkSize)
	}

	if o.percentCompressible <= 0 {
		return fmt.Errorf("circular random: percentCompressible must be > 0, got %d", o.size)
	}
	return nil
}

// RngSeed will set a fixed RNG seed to make usage predictable.
func (o CircularPartialCompressibleOpts) RngSeed(s int64) CircularPartialCompressibleOpts {
	o.seed = &s
	return o
}

// Size will set the size of the circular buffer.
func (o CircularPartialCompressibleOpts) Size(s int) CircularPartialCompressibleOpts {
	o.size = s
	return o
}

type circularPartialCompressibleSrc struct {
	buf []byte
	rng *rand.Rand
	obj Object
	o   Options
	pos int64
}

func newCircularPartialCompressible(o Options) (Source, error) {
	rndSrc := rand.NewSource(int64(rand.Uint64()))
	if o.partialCompr.seed != nil {
		rndSrc = rand.NewSource(*o.partialCompr.seed)
	}
	rng := rand.New(rndSrc)

	size := o.partialCompr.size
	if size <= 0 {
		return nil, fmt.Errorf("size must be > 0, got %d", size)
	}

	// Generate random data for the circular buffer; we will then insert some compressible regions
	buf := make([]byte, size)
	_, err := io.ReadFull(rng, buf)
	if err != nil {
		return nil, err
	}

	chunkCount := 0
	chunkIntervalToMakeCompressible := 100 / o.partialCompr.percentCompressible
	//	fmt.Printf ("setting every %d chunks to be 0. chunk size %d total size %d\n", chunkOrder, o.partialCompr.compressibleChunkSize, size)

	for i := 0; i < size; i += o.partialCompr.compressibleChunkSize {
		//	fmt.Printf("checking offset %d chunk %d!\n", i, chunks)
		chunkCount++
		if chunkCount%chunkIntervalToMakeCompressible != 0 {
			//	fmt.Printf("skipping this chunk; %d %% %d == %d!\n", chunks, chunkOrder, chunks % chunkOrder )
			continue
		}
		if i+o.partialCompr.compressibleChunkSize > size {
			//fmt.Printf("skipping the last chunk so we don't overflow!\n" )
			continue
		}

		for j := i; j < i+o.partialCompr.compressibleChunkSize; j++ {
			// if j == i {
			// 	fmt.Printf("setting the chunk started at %d to 0!\n", j )
			// }
			buf[j] = 0
		}
	}
	// fmt.Printf("dumping the buffer!\n" )
	// for i := 0; i < size; i++	{
	// 	fmt.Printf("%d", buf[i] )
	// }

	r := &circularPartialCompressibleSrc{
		o:   o,
		rng: rng,
		buf: buf,
		obj: Object{
			Reader:      nil,
			Name:        "",
			ContentType: "application/octet-stream",
			Size:        0,
		},
		pos: 0,
	}
	r.obj.setPrefix(o)
	return r, nil
}

func (r *circularPartialCompressibleSrc) Object() *Object {
	var nBuf [16]byte
	randASCIIBytes(nBuf[:], r.rng)
	r.obj.Size = r.o.getSize(r.rng)
	r.obj.setName(fmt.Sprintf("%s.crnd", string(nBuf[:])))

	// Create a new reader for this object, continuing from the current position
	r.obj.Reader = &circularReader{
		buf:    r.buf,
		pos:    r.pos,
		end:    r.pos + r.obj.Size,
		remain: r.obj.Size,
	}

	// Update the position for the next object
	r.pos = (r.pos + r.obj.Size) % int64(len(r.buf))

	return &r.obj
}

func (r *circularPartialCompressibleSrc) String() string {
	if r.o.randSize {
		return fmt.Sprintf("Circular Random data; random size up to %d bytes", r.o.totalSize)
	}
	return fmt.Sprintf("Circular Random data; %d bytes total", r.o.totalSize)
}

func (r *circularPartialCompressibleSrc) Prefix() string {
	return r.obj.Prefix
}

// 32k alternating rand and non-rand
type circularCompressibleReader struct {
	buf    []byte
	pos    int64
	end    int64
	remain int64
}

func (cr *circularCompressibleReader) Read(p []byte) (n int, err error) {
	if cr.remain <= 0 {
		return 0, io.EOF
	}

	if int64(len(p)) > cr.remain {
		p = p[:cr.remain]
	}

	n = len(p)
	for i := range p {
		p[i] = cr.buf[cr.pos%int64(len(cr.buf))]
		cr.pos++
	}

	cr.remain -= int64(n)
	return n, nil
}

func (cr *circularCompressibleReader) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = cr.pos - cr.end + cr.remain + offset
	case io.SeekEnd:
		abs = cr.remain + offset
	default:
		return 0, fmt.Errorf("invalid whence: %d", whence)
	}

	if abs < 0 {
		return 0, fmt.Errorf("negative position: %d", abs)
	}

	if abs > cr.end-cr.pos {
		cr.remain = 0
		return cr.end - cr.pos, io.EOF
	}

	cr.remain = cr.end - cr.pos - abs
	return abs, nil
}
