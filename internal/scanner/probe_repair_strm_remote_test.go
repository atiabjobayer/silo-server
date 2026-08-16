package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestStrmNeedsRemoteProbe(t *testing.T) {
	now := time.Now()

	repaired := &models.MediaFile{
		FilePath:       "/media/movie.strm",
		Container:      "strm",
		ProbeSource:    "strm-remote",
		ProbeUpdatedAt: &now,
		AudioTracks: []models.AudioTrack{
			{Codec: "aac", Language: "hin", Channels: 6},
			{Codec: "aac", Language: "eng", Channels: 6},
		},
	}
	if strmNeedsRemoteProbe(repaired) {
		t.Fatal("strmNeedsRemoteProbe() = true for a remotely probed file")
	}

	placeholder := &models.MediaFile{
		FilePath:       "/media/movie.strm",
		Container:      "strm",
		ProbeSource:    "strm",
		ProbeUpdatedAt: &now,
		AudioTracks:    []models.AudioTrack{{Codec: "aac", Channels: 2}},
	}
	if !strmNeedsRemoteProbe(placeholder) {
		t.Fatal("strmNeedsRemoteProbe() = false for placeholder strm metadata")
	}

	if strmNeedsRemoteProbe(&models.MediaFile{FilePath: "/media/movie.mkv"}) {
		t.Fatal("strmNeedsRemoteProbe() = true for a non-strm file")
	}
	if strmNeedsRemoteProbe(nil) {
		t.Fatal("strmNeedsRemoteProbe() = true for nil file")
	}
}

func TestEnsureStrmRemoteProbeRepairsAudioTracks(t *testing.T) {
	tempDir := t.TempDir()
	ffprobePath := filepath.Join(tempDir, "ffprobe")
	// Fake ffprobe that answers every invocation with a dual-audio probe whose
	// container default is Hindi, so the repair also exercises the
	// English-default normalization. Tier-1 succeeds without any network.
	script := `#!/bin/sh
printf '%s\n' '{"streams":[{"codec_type":"video","codec_name":"h264","width":1280,"height":720,"color_range":"tv"},{"codec_type":"audio","codec_name":"aac","channels":6,"tags":{"language":"hin"},"disposition":{"default":1}},{"codec_type":"audio","codec_name":"aac","channels":6,"tags":{"language":"eng"}}],"format":{"duration":"5400.0","format_name":"matroska,webm"}}'
`
	if err := os.WriteFile(ffprobePath, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake ffprobe: %v", err)
	}

	strmPath := filepath.Join(tempDir, "movie.strm")
	if err := os.WriteFile(strmPath, []byte("http://127.0.0.1/movie.mkv\n"), 0o644); err != nil {
		t.Fatalf("writing .strm file: %v", err)
	}

	file := &models.MediaFile{
		ID:          1,
		FilePath:    strmPath,
		Container:   "strm",
		ProbeSource: "strm",
		Duration:    7200,
		AudioTracks: []models.AudioTrack{
			{Codec: "aac", Channels: 2},
		},
	}

	ensurer := &PlaybackProbeEnsurer{ffprobePath: ffprobePath, timeout: 15 * time.Second}
	repaired := ensurer.ensureStrmProbe(context.Background(), file)
	if repaired == file {
		t.Fatal("ensureStrmProbe() returned the un-repaired placeholder file")
	}
	if repaired.ProbeSource != "strm-remote" {
		t.Fatalf("ProbeSource = %q, want strm-remote", repaired.ProbeSource)
	}
	if len(repaired.AudioTracks) != 2 {
		t.Fatalf("AudioTracks = %d, want 2", len(repaired.AudioTracks))
	}
	if repaired.AudioTracks[0].Default || !repaired.AudioTracks[1].Default {
		t.Fatalf("English track should become default: tracks = %+v", repaired.AudioTracks)
	}
	if len(repaired.VideoTracks) != 1 || repaired.VideoTracks[0].Width != 1280 {
		t.Fatalf("unexpected video tracks: %+v", repaired.VideoTracks)
	}
	if repaired.Container != "mkv" {
		t.Fatalf("Container = %q, want mkv", repaired.Container)
	}
}

func TestPreferEnglishAudioDefaultStrm(t *testing.T) {
	hindiDefault := &ProbeData{AudioTracks: []AudioTrackInfo{
		{Codec: "aac", Language: "hin", Default: true},
		{Codec: "aac", Language: "eng", Default: false},
	}}
	preferEnglishAudioDefaultStrm(hindiDefault)
	if hindiDefault.AudioTracks[0].Default || !hindiDefault.AudioTracks[1].Default {
		t.Fatalf("English should replace Hindi default: %+v", hindiDefault.AudioTracks)
	}

	englishDefault := &ProbeData{AudioTracks: []AudioTrackInfo{
		{Codec: "aac", Language: "hin", Default: false},
		{Codec: "aac", Language: "eng", Default: true},
	}}
	preferEnglishAudioDefaultStrm(englishDefault)
	if englishDefault.AudioTracks[0].Default || !englishDefault.AudioTracks[1].Default {
		t.Fatalf("existing English default must be kept: %+v", englishDefault.AudioTracks)
	}

	noEnglish := &ProbeData{AudioTracks: []AudioTrackInfo{
		{Codec: "aac", Language: "hin", Default: true},
		{Codec: "aac", Language: "ben", Default: false},
	}}
	preferEnglishAudioDefaultStrm(noEnglish)
	if !noEnglish.AudioTracks[0].Default || noEnglish.AudioTracks[1].Default {
		t.Fatalf("without an English track the default must stay: %+v", noEnglish.AudioTracks)
	}
}

func TestEnsureStrmRemoteProbeKeepsPlaceholderOnProbeFailure(t *testing.T) {
	tempDir := t.TempDir()
	ffprobePath := filepath.Join(tempDir, "ffprobe")
	if err := os.WriteFile(ffprobePath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("writing failing fake ffprobe: %v", err)
	}

	strmPath := filepath.Join(tempDir, "movie.strm")
	if err := os.WriteFile(strmPath, []byte("http://127.0.0.1/movie.mkv\n"), 0o644); err != nil {
		t.Fatalf("writing .strm file: %v", err)
	}

	file := &models.MediaFile{
		ID:          1,
		FilePath:    strmPath,
		Container:   "strm",
		ProbeSource: "strm",
		AudioTracks: []models.AudioTrack{
			{Codec: "aac", Channels: 2},
		},
	}

	ensurer := &PlaybackProbeEnsurer{ffprobePath: ffprobePath, timeout: 15 * time.Second}
	repaired := ensurer.ensureStrmProbe(context.Background(), file)
	if repaired != file {
		t.Fatal("ensureStrmProbe() replaced the file despite probe failure")
	}
}
