package icalendar

import (
	"fmt"
)

// Unfold joins folded content lines per RFC 5545.
//
// ../rfc/5545:3.1
func Unfold(data []byte) ([]byte, error) {
	var out []byte
	for i := 0; i < len(data); i++ {
		if data[i] == 0 {
			return nil, fmt.Errorf("icalendar: NUL byte")
		}
		switch data[i] {
		case '\r':
			if i+1 < len(data) && data[i+1] == '\n' {
				if i+2 < len(data) && isWSP(data[i+2]) {
					// ../rfc/5545:3.1
					i += 2
					continue
				}
				out = append(out, '\r', '\n')
				i++
				continue
			}
			out = append(out, '\r')
		case '\n':
			if i+1 < len(data) && isWSP(data[i+1]) {
				// ../rfc/5545:3.1
				i++
				continue
			}
			out = append(out, '\n')
		default:
			out = append(out, data[i])
		}
	}
	return out, nil
}

func isWSP(b byte) bool {
	return b == ' ' || b == '\t'
}
