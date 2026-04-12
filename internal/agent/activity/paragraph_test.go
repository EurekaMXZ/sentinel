package activity

import (
	"reflect"
	"testing"
)

func TestParagraphBufferEmitsCompleteParagraphs(t *testing.T) {
	var buffer paragraphBuffer
	if chunks := buffer.Write("Hello"); len(chunks) != 0 {
		t.Fatalf("partial paragraph emitted early: %q", chunks)
	}
	chunks := buffer.Write(" world\n\nNext")
	if !reflect.DeepEqual(chunks, []string{"Hello world\n\n"}) {
		t.Fatalf("chunks=%q", chunks)
	}
	if remaining := buffer.Flush(); !reflect.DeepEqual(remaining, []string{"Next"}) {
		t.Fatalf("remaining=%q", remaining)
	}
}

func TestParagraphBufferHandlesSplitNewlines(t *testing.T) {
	var buffer paragraphBuffer
	if chunks := buffer.Write("A\n"); len(chunks) != 0 {
		t.Fatalf("unexpected chunks=%q", chunks)
	}
	chunks := buffer.Write("\nB\n\nC")
	if !reflect.DeepEqual(chunks, []string{"A\n\n", "B\n\n"}) {
		t.Fatalf("chunks=%q", chunks)
	}
	if remaining := buffer.Flush(); !reflect.DeepEqual(remaining, []string{"C"}) {
		t.Fatalf("remaining=%q", remaining)
	}
}

func TestParagraphBufferKeepsFencedCodeTogether(t *testing.T) {
	var buffer paragraphBuffer
	chunks := buffer.Write("before\n\n```go\nfmt.Println(\"a\")\n\nfmt.Println(\"b\")\n```\n\nafter")
	if !reflect.DeepEqual(chunks, []string{"before\n\n", "```go\nfmt.Println(\"a\")\n\nfmt.Println(\"b\")\n```\n\n"}) {
		t.Fatalf("chunks=%q", chunks)
	}
	if remaining := buffer.Flush(); !reflect.DeepEqual(remaining, []string{"after"}) {
		t.Fatalf("remaining=%q", remaining)
	}
}

func TestParagraphBufferFlushesUnterminatedFence(t *testing.T) {
	var buffer paragraphBuffer
	if chunks := buffer.Write("```go\nunfinished\n"); len(chunks) != 0 {
		t.Fatalf("unexpected chunks=%q", chunks)
	}
	if remaining := buffer.Flush(); !reflect.DeepEqual(remaining, []string{"```go\nunfinished\n"}) {
		t.Fatalf("remaining=%q", remaining)
	}
}

func TestParagraphBufferIgnoresWhitespaceOnly(t *testing.T) {
	var buffer paragraphBuffer
	if chunks := buffer.Write("\n\n  \n"); len(chunks) != 0 {
		t.Fatalf("unexpected chunks=%q", chunks)
	}
	if remaining := buffer.Flush(); len(remaining) != 0 {
		t.Fatalf("remaining=%q", remaining)
	}
}
