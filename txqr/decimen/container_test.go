package decimen

import (
	"bytes"
	"testing"
)

func TestContainerRoundTrip(t *testing.T) {
	data := testPayload(10000)
	out, gz, err := PackContainer("hello.txt", "text/plain", data)
	if err != nil {
		t.Fatal(err)
	}
	if !gz && len(out) != 49+len("hello.txt")+len("text/plain")+len(data) {
		t.Fatalf("container length %d unexpected", len(out))
	}
	if gz && len(out) >= 49+len("hello.txt")+len("text/plain")+len(data) {
		t.Fatalf("gzipped container must be smaller, got %d", len(out))
	}
	c, err := UnpackContainer(out)
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "hello.txt" || c.Type != "text/plain" || !bytes.Equal(c.Bytes, data) {
		t.Fatalf("container round-trip mismatch: name=%q type=%q", c.Name, c.Type)
	}
	if c.FileLen != uint32(len(data)) {
		t.Fatalf("fileLen = %d, want %d", c.FileLen, len(data))
	}
	if !gz {
		t.Fatal("expected gzip for compressible text")
	}

	if !bytes.Equal(c.Bytes, data) {
		t.Fatal("gzip bytes mismatch")
	}
	_ = out
}

func TestContainerNoGzipPrecompressed(t *testing.T) {
	data := testPayload(50000)
	out, gz, err := PackContainer("photo.jpg", "image/jpeg", data)
	if err != nil {
		t.Fatal(err)
	}
	if gz {
		t.Fatal("image/jpeg must not be gzipped")
	}
	c, err := UnpackContainer(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(c.Bytes, data) {
		t.Fatal("jpeg bytes mismatch")
	}
}

func TestContainerSmallNoGzip(t *testing.T) {
	data := testPayload(100)
	out, gz, err := PackContainer("a.bin", "application/octet-stream", data)
	if err != nil {
		t.Fatal(err)
	}
	if gz {
		t.Fatal("small file must not be gzipped")
	}
	c, err := UnpackContainer(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(c.Bytes, data) {
		t.Fatal("small bytes mismatch")
	}
}

func TestContainerBadMagic(t *testing.T) {
	if _, err := UnpackContainer(make([]byte, 50)); err == nil {
		t.Fatal("bad magic accepted")
	}
	if _, err := UnpackContainer([]byte("DCF2")); err == nil {
		t.Fatal("short container accepted")
	}
}

func TestSanitizeFileName(t *testing.T) {
	cases := map[string]string{
		"../evil/../x.txt": "x.txt",
		"  hello.txt  ":    "hello.txt",
		"/abs/path/f":      "f",
		".":                "transfer.bin",
		"":                 "transfer.bin",
		"a b.txt":          "a b.txt",
	}
	for in, want := range cases {
		if got := SanitizeFileName(in); got != want {
			t.Fatalf("SanitizeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}
