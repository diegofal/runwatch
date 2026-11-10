package execx

import "strings"

const esc = 0x1b

// stripANSI is s without the escape sequences programs print for colour,
// cursor moves, titles and links: CSI (ESC [ … final byte), the string
// sequences OSC, DCS, SOS, PM and APC (ended by BEL or ESC \), and the short
// ESC sequences. Programs colour output into a pipe too, and what execx
// captures is read by people in runwatch.log, notifications and the CLI,
// where the codes are noise. Bytes outside a sequence are kept as
// they are, so UTF-8 survives; a sequence cut off by the end of s is dropped.
func stripANSI(s string) string {
	if strings.IndexByte(s, esc) < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == esc {
			i = skipEscape(s, i+1)
			continue
		}
		next := strings.IndexByte(s[i:], esc)
		if next < 0 {
			next = len(s) - i
		}
		b.WriteString(s[i : i+next])
		i += next
	}
	return b.String()
}

// skipEscape returns where the sequence whose ESC is just before i ends.
// An ESC followed by nothing a sequence can start with is dropped alone.
func skipEscape(s string, i int) int {
	if i == len(s) {
		return i
	}
	switch s[i] {
	case '[':
		return skipCSI(s, i+1)
	case ']', 'P', 'X', '^', '_':
		return skipString(s, i+1)
	}
	for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
		i++
	}
	if i < len(s) && s[i] >= 0x30 && s[i] <= 0x7e {
		i++
	}
	return i
}

// skipCSI skips parameter and intermediate bytes (0x20–0x3f) and the final
// byte (0x40–0x7e). Any other byte, a line end say, ends a broken sequence
// and is kept.
func skipCSI(s string, i int) int {
	for ; i < len(s); i++ {
		switch c := s[i]; {
		case c >= 0x40 && c <= 0x7e:
			return i + 1
		case c < 0x20 || c > 0x3f:
			return i
		}
	}
	return i
}

// skipString skips a string sequence up to BEL or ST (ESC \). A line end
// ends an unterminated one and is kept, so it can't swallow the lines after
// it; another ESC starts the next sequence.
func skipString(s string, i int) int {
	for ; i < len(s); i++ {
		switch s[i] {
		case 0x07:
			return i + 1
		case '\n':
			return i
		case esc:
			if i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
			return i
		}
	}
	return i
}
