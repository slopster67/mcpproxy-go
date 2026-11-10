package logs

import (
	"io"
	"os"
	"strings"
)

// ReadTail scans backwards so a live viewer reads only the requested tail.
// Each read has a fixed end offset; growth and rotation are picked up next time.
func ReadTail(path string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}
	end := stat.Size()
	if end == 0 {
		return []string{}, nil
	}
	position, start, remaining := end, int64(0), limit
	buffer := make([]byte, 64*1024)
scan:
	for position > 0 {
		size := min(int64(len(buffer)), position)
		position -= size
		n, err := file.ReadAt(buffer[:size], position)
		if err != nil && err != io.EOF {
			return nil, err
		}
		for i := n - 1; i >= 0; i-- {
			if buffer[i] == '\n' && position+int64(i) != end-1 {
				remaining--
				if remaining == 0 {
					start = position + int64(i) + 1
					break scan
				}
			}
		}
	}
	data, err := io.ReadAll(io.NewSectionReader(file, start, end-start))
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	return lines, nil
}
