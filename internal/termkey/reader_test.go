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

	key, err := Read(reader, make([]byte, 16))
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

	key, err := Read(reader, make([]byte, 16))
	if err != nil {
		t.Fatal(err)
	}
	if key != "\x1b" {
		t.Fatalf("key = %q", key)
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
