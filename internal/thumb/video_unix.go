//go:build !windows

package thumb

// stdinInput names ffmpeg's stdin as a file. /dev/stdin is the open file
// itself (reopened on Linux, duplicated on macOS), so ffmpeg can seek in it;
// "pipe:0" would make it read the file as a stream.
const stdinInput = "file:/dev/stdin"
