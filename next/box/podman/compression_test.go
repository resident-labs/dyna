package podman

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestBinaryCacheCompressesOncePerEncodingAndRoundTrips(t *testing.T) {
	raw := bytes.Repeat([]byte("Dyna binary content"), 1000)
	reads := 0
	cache := NewBinaryCache(func() ([]byte, error) { reads++; return raw, nil })
	for _, encoding := range []BinaryEncoding{BinaryZstd, BinaryGzip} {
		packed, err := cache.Bytes(encoding)
		if err != nil {
			t.Fatal(err)
		}
		again, err := cache.Bytes(encoding)
		if err != nil || &packed[0] != &again[0] {
			t.Fatalf("cache not reused for %s: %v", encoding, err)
		}
		var decoded []byte
		if encoding == BinaryZstd {
			reader, err := zstd.NewReader(nil)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err = reader.DecodeAll(packed, nil)
			reader.Close()
			if err != nil {
				t.Fatal(err)
			}
		} else {
			reader, err := gzip.NewReader(bytes.NewReader(packed))
			if err != nil {
				t.Fatal(err)
			}
			decoded, err = io.ReadAll(reader)
			reader.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
		if !bytes.Equal(decoded, raw) {
			t.Fatalf("%s roundtrip changed bytes", encoding)
		}
	}
	if reads != 2 {
		t.Fatalf("read/compression count %d, want once per encoding", reads)
	}
}

type codecRunner struct{ codec string }

func (r codecRunner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	for _, arg := range args {
		if arg == "-O" || arg == "-M" {
			return nil, nil
		}
	}
	body := ""
	if strings.Contains(args[len(args)-1], "command -v zstd") {
		body = r.codec
	}
	return []byte(fmt.Sprintf("%sDYNA-END %020d 000\n", body, len(body))), nil
}
func TestBinaryCodecNegotiationAndMissingTools(t *testing.T) {
	for _, codec := range []string{"zstd", "gzip", ""} {
		t.Run(codec, func(t *testing.T) {
			h, err := New("user@host", t.TempDir(), "host", codecRunner{codec: codec})
			if err != nil {
				t.Fatal(err)
			}
			called := false
			err = h.StageBinary(context.Background(), "/host/bin", strings.Repeat("a", 64), func(encoding BinaryEncoding) ([]byte, error) {
				called = true
				if string(encoding) != codec {
					t.Fatalf("codec %s, want %s", encoding, codec)
				}
				return []byte("packed"), nil
			})
			if codec == "" {
				var missing BinaryDecompressorMissing
				if !errors.As(err, &missing) || called {
					t.Fatalf("missing tool boundary: %v, read=%v", err, called)
				}
			} else if err != nil || !called {
				t.Fatalf("codec %s: %v, read=%v", codec, err, called)
			}
		})
	}
}

// A portable zstd executable for the remote-shell test, backed by the same existing decoder dependency. A CI image needs
// no new decompressor to exercise the production zstd shell path; the owning probe still checks --version before use.
func TestBinaryZstdDecoderHelper(t *testing.T) {
	if os.Getenv("DYNA_TEST_ZSTD_DECODER") != "1" {
		return
	}
	reader, err := zstd.NewReader(os.Stdin)
	if err != nil {
		os.Exit(1)
	}
	if _, err := io.Copy(os.Stdout, reader); err != nil {
		os.Exit(1)
	}
	reader.Close()
	os.Exit(0)
}
func TestZstdStageUnpacksExactBytesAndGzipFallback(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, encoding := range []BinaryEncoding{BinaryZstd, BinaryGzip} {
		t.Run(string(encoding), func(t *testing.T) {
			script := "exit 127"
			if encoding == BinaryZstd {
				script = `if [ "$1" = --version ]; then echo test-zstd; exit 0; fi
DYNA_TEST_ZSTD_DECODER=1 exec ` + quote(executable) + ` -test.run=^TestBinaryZstdDecoderHelper$`
			}
			tools := fakeTools(t, map[string]string{"zstd": script})
			t.Setenv("PATH", tools+":"+os.Getenv("PATH"))
			h, err := New("user@host", t.TempDir(), "host", &localBinaryRunner{})
			if err != nil {
				t.Fatal(err)
			}
			raw := bytes.Repeat([]byte("production format"), 1000)
			sumBytes := sha256.Sum256(raw)
			sum := hex.EncodeToString(sumBytes[:])
			bin := t.TempDir()
			if err := h.StageBinary(context.Background(), bin, sum, func(codec BinaryEncoding) ([]byte, error) {
				if codec != encoding {
					t.Fatalf("selected %s", codec)
				}
				return CompressBinary(raw, codec)
			}); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(bin, sum, "dyna"))
			if err != nil || !bytes.Equal(got, raw) {
				t.Fatalf("unpacked %s: %v", encoding, err)
			}
		})
	}
}
