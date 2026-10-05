//go:build !unix

package fsview

import "errors"

func mkfifo(string) error { return errors.New("no FIFOs on this platform") }
