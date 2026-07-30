package scanner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadStrmURL(t *testing.T) {
	tmp := t.TempDir()

	t.Run("valid http url", func(t *testing.T) {
		path := filepath.Join(tmp, "movie.strm")
		if err := os.WriteFile(path, []byte("http://example.com/movie.mkv"), 0o644); err != nil {
			t.Fatal(err)
		}
		url, err := readStrmURL(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if url != "http://example.com/movie.mkv" {
			t.Fatalf("got %q, want %q", url, "http://example.com/movie.mkv")
		}
	})

	t.Run("valid https url", func(t *testing.T) {
		path := filepath.Join(tmp, "show.strm")
		if err := os.WriteFile(path, []byte("https://cdn.example.com/episode.mkv"), 0o644); err != nil {
			t.Fatal(err)
		}
		url, err := readStrmURL(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if url != "https://cdn.example.com/episode.mkv" {
			t.Fatalf("got %q, want %q", url, "https://cdn.example.com/episode.mkv")
		}
	})

	t.Run("trims whitespace", func(t *testing.T) {
		path := filepath.Join(tmp, "trim.strm")
		if err := os.WriteFile(path, []byte("  https://example.com/video.mp4  \n"), 0o644); err != nil {
			t.Fatal(err)
		}
		url, err := readStrmURL(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if url != "https://example.com/video.mp4" {
			t.Fatalf("got %q, want clean url", url)
		}
	})

	t.Run("rejects empty file", func(t *testing.T) {
		path := filepath.Join(tmp, "empty.strm")
		if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := readStrmURL(path)
		if err == nil {
			t.Fatal("expected error for empty file")
		}
	})

	t.Run("rejects multiline", func(t *testing.T) {
		path := filepath.Join(tmp, "multi.strm")
		if err := os.WriteFile(path, []byte("http://a.com\nhttp://b.com"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := readStrmURL(path)
		if err == nil {
			t.Fatal("expected error for multiline content")
		}
	})

	t.Run("rejects non-http scheme", func(t *testing.T) {
		path := filepath.Join(tmp, "bad.strm")
		if err := os.WriteFile(path, []byte("ftp://example.com/file.mkv"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := readStrmURL(path)
		if err == nil {
			t.Fatal("expected error for non-http scheme")
		}
	})

	t.Run("missing file", func(t *testing.T) {
		_, err := readStrmURL("/nonexistent/file.strm")
		if err == nil {
			t.Fatal("expected error for missing file")
		}
	})
}
