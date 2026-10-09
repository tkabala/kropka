// Command _qr writes the QR code kropka prints for a URL as a PNG, in the
// demo terminal's colors, for the phone's viewfinder in the demo. It uses the
// library and level kropka does, so it's the same code.
//
//	go run ./docs/demo/_qr <url> <file.png>
package main

import (
	"fmt"
	"image/color"
	"os"

	"github.com/skip2/go-qrcode"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: _qr <url> <file.png>")
		os.Exit(2)
	}
	q, err := qrcode.New(os.Args[1], qrcode.Low)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	q.ForegroundColor = color.RGBA{0x1e, 0x1e, 0x2e, 0xff} // Catppuccin Mocha: the background
	q.BackgroundColor = color.RGBA{0xcd, 0xd6, 0xf4, 0xff} // and the text
	if err := q.WriteFile(-8, os.Args[2]); err != nil { // 8 px per module
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
