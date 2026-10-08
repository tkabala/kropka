//go:build windows

package thumb

// stdinInput names ffmpeg's stdin as a file descriptor: the fd protocol
// (ffmpeg 6.0+) reads stdin unless told otherwise with -fd, and seeks in it
// when it is a file. "pipe:0" would make ffmpeg read the file as a stream.
const stdinInput = "fd:"
