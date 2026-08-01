package termkey

import (
	"os"
	"testing"
	"time"
)

func TestReadJoinsFragmentedArrowSequence(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()

	go func() {
		_, _ = writer.Write([]byte("\x1b"))
		time.Sleep(5 * time.Millisecond)
		_, _ = writer.Write([]byte("[A"))
	}()

	key, err := NewReader(reader).Read()
	if err != nil {
		t.Fatal(err)
	}
	if key != "\x1b[A" {
		t.Fatalf("key = %q", key)
	}
}

func TestReadReturnsStandaloneEscape(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("anonymous pipes do not have Windows console wait semantics")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	if _, err := writer.Write([]byte("\x1b")); err != nil {
		t.Fatal(err)
	}

	key, err := NewReader(reader).Read()
	if err != nil {
		t.Fatal(err)
	}
	if key != "\x1b" {
		t.Fatalf("key = %q", key)
	}
}

func TestReaderKeepsBatchedKeysForFollowingReads(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	if _, err := writer.Write([]byte("\x1b[A\x1b[B")); err != nil {
		t.Fatal(err)
	}

	keys := NewReader(reader)
	first, err := keys.Read()
	if err != nil {
		t.Fatal(err)
	}
	second, err := keys.Read()
	if err != nil {
		t.Fatal(err)
	}
	if first != "\x1b[A" || second != "\x1b[B" {
		t.Fatalf("keys = %q, %q", first, second)
	}
}

func TestReaderReturnsTrailingEscapeFromBatch(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("anonymous pipes do not have Windows console wait semantics")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	if _, err := writer.Write([]byte("\x1b[A\x1b")); err != nil {
		t.Fatal(err)
	}

	keys := NewReader(reader)
	first, err := keys.Read()
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	second, err := keys.Read()
	if err != nil {
		t.Fatal(err)
	}
	if first != "\x1b[A" || second != "\x1b" {
		t.Fatalf("keys = %q, %q", first, second)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("trailing Escape took %s to return", elapsed)
	}
}

func TestReaderNormalizesEnhancedCursorKeys(t *testing.T) {
	tests := map[string]string{
		"\x1b[1;3A":   "\x1b[A",
		"\x1b[1;2B":   "\x1b[B",
		"\x1b[1;3:1A": "\x1b[A",
		"\x1b[;3B":    "\x1b[B",
		"\x1bOC":      "\x1b[C",
		"\x1b[1;5D":   "\x1b[D",
	}
	for input, want := range tests {
		if got := normalizeKey(input); got != want {
			t.Errorf("normalizeKey(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestReaderDrainDiscardsBufferedInput(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("anonymous pipes do not have Windows console wait semantics")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	if _, err := writer.Write([]byte("\x1b[A\x1b[B")); err != nil {
		t.Fatal(err)
	}

	keys := NewReader(reader)
	if err := keys.Drain(); err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() {
		key, _ := keys.Read()
		done <- key
	}()
	if _, err := writer.Write([]byte("n")); err != nil {
		t.Fatal(err)
	}
	select {
	case key := <-done:
		if key != "n" {
			t.Fatalf("key after drain = %q, want n", key)
		}
	case <-time.After(time.Second):
		t.Fatal("reader did not resume after drain")
	}
}

func TestIncompleteEscapeSequence(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"\x1b", true},
		{"\x1b[", true},
		{"\x1b[1;", true},
		{"\x1b[A", false},
		{"\x1b[1;2A", false},
		{"q", false},
	}
	for _, test := range tests {
		if got := incompleteEscapeSequence([]byte(test.input)); got != test.want {
			t.Errorf("incompleteEscapeSequence(%q) = %v, want %v", test.input, got, test.want)
		}
	}
}
