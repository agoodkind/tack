package overlay

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
)

// maxFileBytes bounds the memory that the extracted file can use.
const maxFileBytes = 16 << 20

var (
	errNotRegular = errors.New("path is not a regular file in the merged image")
	errTooLarge   = errors.New("file exceeds the extraction size limit")
)

// scan records the links and the requested file content from one pass over the merged filesystem.
type scan struct {
	links   map[string]link
	content []byte
	found   bool
}

// readFile returns the bytes of the file at imagePath in the merged image filesystem.
// The first pass records every link and keeps only the file at the requested path.
// A second pass runs only when link resolution identifies a different path.
func readFile(ctx context.Context, image v1.Image, imagePath string) ([]byte, error) {
	requested := normalize(imagePath)
	first, err := walk(ctx, image, requested)
	if err != nil {
		return nil, err
	}
	resolved, err := resolve(first.links, imagePath)
	if err != nil {
		return nil, err
	}
	result := first
	if resolved != requested {
		result, err = walk(ctx, image, resolved)
		if err != nil {
			return nil, err
		}
	}
	if !result.found {
		return nil, fmt.Errorf("%s: %w", resolved, errNotRegular)
	}
	return result.content, nil
}

// walk reads the merged filesystem once. It records every link and keeps the content of the
// regular file at wanted. Only that file counts toward the size limit.
func walk(ctx context.Context, image v1.Image, wanted string) (scan, error) {
	result := scan{links: make(map[string]link), content: nil, found: false}
	stream := mutate.Extract(image)
	reader := tar.NewReader(stream)
	for {
		if err := ctx.Err(); err != nil {
			_ = stream.Close()
			return scan{}, fmt.Errorf("read image archive: %w", err)
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = stream.Close()
			return scan{}, fmt.Errorf("read image archive: %w", err)
		}
		entry := normalize(header.Name)
		switch header.Typeflag {
		case tar.TypeSymlink:
			result.links[entry] = link{target: header.Linkname, hard: false}
		case tar.TypeLink:
			result.links[entry] = link{target: header.Linkname, hard: true}
		case tar.TypeReg:
			if entry == wanted {
				if err := keepFile(&result, reader, header, entry); err != nil {
					_ = stream.Close()
					return scan{}, err
				}
			}
		}
	}
	if err := stream.Close(); err != nil {
		return scan{}, fmt.Errorf("close image archive: %w", err)
	}
	return result, nil
}

// keepFile stores the regular file content when it is within the size limit.
func keepFile(result *scan, reader io.Reader, header *tar.Header, entry string) error {
	if header.Size > maxFileBytes {
		return fmt.Errorf("%s: %w", entry, errTooLarge)
	}
	content, err := io.ReadAll(io.LimitReader(reader, maxFileBytes))
	if err != nil {
		return fmt.Errorf("read %s: %w", entry, err)
	}
	result.content = content
	result.found = true
	return nil
}
