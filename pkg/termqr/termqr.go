// Package termqr draws a QR code in a terminal, for a phone's camera.
package termqr

import (
	"strings"

	"rsc.io/qr"
)

// Draw is s as a QR code in half blocks, two modules a row, black on white
// with the white edge a reader needs.
func Draw(s string) (string, error) {
	code, err := qr.Encode(s, qr.L)
	if err != nil {
		return "", err
	}
	const quiet = 4
	var b strings.Builder
	for y := -quiet; y < code.Size+quiet; y += 2 {
		b.WriteString("\x1b[30;47m")
		for x := -quiet; x < code.Size+quiet; x++ {
			b.WriteString([]string{" ", "▄", "▀", "█"}[b2i(code.Black(x, y))<<1|b2i(code.Black(x, y+1))])
		}
		b.WriteString("\x1b[0m\n")
	}
	return b.String(), nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
