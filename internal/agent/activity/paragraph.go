package activity

import "strings"

// paragraphBuffer turns token deltas into Markdown paragraph chunks. A chunk is
// emitted only after a blank line outside a fenced code block, so the browser
// can render a complete paragraph at once without splitting code blocks.
type paragraphBuffer struct {
	pending string
	scan    int
	fence   string
}

// Write appends a delta and returns every paragraph that is now complete.
func (b *paragraphBuffer) Write(delta string) []string {
	b.pending += delta
	var chunks []string
	for {
		index := strings.IndexByte(b.pending[b.scan:], '\n')
		if index < 0 {
			break
		}
		end := b.scan + index + 1
		line := strings.TrimRight(b.pending[b.scan:end], "\r\n")
		b.scan = end

		if marker := fenceMarker(line); marker != "" {
			if b.fence == "" {
				b.fence = marker
			} else if strings.HasPrefix(strings.TrimLeft(line, " \t"), b.fence) {
				b.fence = ""
			}
			continue
		}
		if b.fence == "" && strings.TrimSpace(line) == "" {
			chunk := b.pending[:end]
			if strings.TrimSpace(chunk) != "" {
				chunks = append(chunks, chunk)
			}
			b.pending = b.pending[end:]
			b.scan = 0
		}
	}
	return chunks
}

// Flush returns the remaining text, including an unterminated code fence.
func (b *paragraphBuffer) Flush() []string {
	chunk := b.pending
	b.pending = ""
	b.scan = 0
	b.fence = ""
	if strings.TrimSpace(chunk) == "" {
		return nil
	}
	return []string{chunk}
}

func fenceMarker(line string) string {
	trimmed := strings.TrimLeft(line, " \t")
	switch {
	case strings.HasPrefix(trimmed, "```"):
		return "```"
	case strings.HasPrefix(trimmed, "~~~"):
		return "~~~"
	default:
		return ""
	}
}
