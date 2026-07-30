package naming

import (
	"testing"
)

func TestStripInferReleaseTokens(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "year range with TV Series notation",
			input: "10 Things I Hate About You (TV Series 2009–2010) 720p",
			want:  "10 Things I Hate About You",
		},
		{
			name:  "year range with dash",
			input: "The Shield (2002-2008) 1080p BluRay x265",
			want:  "The Shield",
		},
		{
			name:  "dual audio bracket tag",
			input: "Supergirl (2026) 720p [Dual Audio] [Hindi (Clean)+English CAM] -HDHub",
			want:  "Supergirl (2026)",
		},
		{
			name:  "Hindi English dual audio",
			input: "Wardriver (2026) 720p AMZN-WEB x264 ESub [Dual Audio][Hindi 2.0+English 5.1] -MsMod",
			want:  "Wardriver (2026)",
		},
		{
			name:  "ESub tag",
			input: "No Strings Attached (2011) 720p BluRay x264 ESub [Dual Audio][Hindi 5.1+English 5.1] -mkvC",
			want:  "No Strings Attached (2011)",
		},
		{
			name:  "RARBG suffix",
			input: "The.Shield.S06E02.1080p.BluRay.x265-RARBG",
			want:  "The.Shield.S06E02",
		},
		{
			name:  "scene release suffix",
			input: "Movie.Name.2024.1080p.AMZN.WEB-DL.DDP5.1.H.264-FLUX",
			want:  "Movie.Name.2024",
		},
		{
			name:  "HDTC source",
			input: "Some Movie 2026 720p HDTC V2 x264 Dual Audio -HDHub",
			want:  "Some Movie 2026",
		},
		{
			name:  "unrated edition",
			input: "Movie Title (2025) Unrated 1080p BluRay x264",
			want:  "Movie Title (2025)",
		},
		{
			name:  "extended edition",
			input: "The Film 2024 Extended Director's Cut 2160p HDR",
			want:  "The Film 2024",
		},
		{
			name:  "proper repack",
			input: "Show Name S01E01 PROPER 1080p x264-RARBG",
			want:  "Show Name S01E01",
		},
		{
			name:  "clean title with year",
			input: "Breaking Bad (2008)",
			want:  "",
		},
		{
			name:  "already clean",
			input: "The Matrix",
			want:  "",
		},
		{
			name:  "multiple codecs and formats",
			input: "Avatar 2009 2160p HDR DV TrueHD Atmos x265-NTb",
			want:  "Avatar 2009",
		},
		{
			name:  "DDP5.1 audio",
			input: "Movie 2023 1080p WEB-DL DDP5.1 H.264-EVO",
			want:  "Movie 2023",
		},
		{
			name:  "YIFY release",
			input: "Inception 2010 1080p BrRip x264-YIFY",
			want:  "Inception 2010",
		},
		{
			name:  "Tamil dual audio",
			input: "Vikram (2022) 1080p [Tamil + Hindi] x264 ESub",
			want:  "Vikram (2022)",
		},
		{
			name:  "leading number in title survives",
			input: "2 Broke Girls (TV Series 2011–2017) 720p",
			want:  "2 Broke Girls",
		},
		{
			name:  "leading number movie",
			input: "12 Angry Men (1957) 1080p x264",
			want:  "12 Angry Men (1957)",
		},
		{
			name:  "star and em dash in parent dir (not in title itself)",
			input: "2 Broke Girls (TV Series 2011–2017) 720p [Dual Audio] ESub",
			want:  "2 Broke Girls",
		},
		{
			name:  "open-ended year range",
			input: "1000 Babies (TV Series 2024–) 720p [Dual Audio]",
			want:  "1000 Babies",
		},
		{
			name:  "tv mini series single year",
			input: "11.22.63 (TV Mini Series 2016) 1080p",
			want:  "11.22.63",
		},
		{
			name:  "tv series single year no range",
			input: "Show Name (TV Series 2024) 1080p x264",
			want:  "Show Name",
		},
		{
			name:  "plain year range no tv series text",
			input: "Movie Title (2009–2010) 720p BluRay",
			want:  "Movie Title",
		},
		{
			name:  "open-ended plain year range",
			input: "Some Show (2024–) 1080p",
			want:  "Some Show",
		},
		{
			name:  "year in parens alone is NOT stripped",
			input: "Breaking Bad (2008) 1080p x265",
			want:  "Breaking Bad (2008)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripInferReleaseTokens(tt.input)
			if got != tt.want {
				t.Errorf("stripInferReleaseTokens(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseInferTitleYear_WithCleaning(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantTitle string
		wantYear  int
	}{
		{
			name:      "TV series year range",
			input:     "10 Things I Hate About You (TV Series 2009–2010) 720p [Dual Audio]",
			wantTitle: "10 Things I Hate About You",
			wantYear:  0,
		},
		{
			name:      "clean year extraction survives cleaning",
			input:     "Breaking Bad (2008) 1080p x265-RARBG",
			wantTitle: "Breaking Bad",
			wantYear:  2008,
		},
		{
			name:      "dual audio movie folder",
			input:     "Wardriver (2026) 720p AMZN-WEB x264 ESub [Dual Audio][Hindi 2.0+English 5.1] -MsMod",
			wantTitle: "Wardriver",
			wantYear:  2026,
		},
		{
			name:      "no year in clean",
			input:     "The Matrix 1080p BluRay x264",
			wantTitle: "The Matrix",
			wantYear:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			title, year := parseInferTitleYear(tt.input)
			if title != tt.wantTitle {
				t.Errorf("title = %q, want %q", title, tt.wantTitle)
			}
			if year != tt.wantYear {
				t.Errorf("year = %d, want %d", year, tt.wantYear)
			}
		})
	}
}
