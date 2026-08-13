package provider

import (
	"reflect"
	"testing"

	"golang.org/x/time/rate"
)

func TestShokoClientUsesModerateRateLimit(t *testing.T) {
	t.Parallel()

	client := newShokoClient("http://shoko:8111", "", TagFilterConfig{})
	if got := client.limiter.Limit(); got != rate.Limit(shokoRequestsPerSecond) {
		t.Fatalf("limiter rate = %v, want %v", got, shokoRequestsPerSecond)
	}
	if got := client.limiter.Burst(); got != shokoRequestBurst {
		t.Fatalf("limiter burst = %d, want %d", got, shokoRequestBurst)
	}
}

func TestTitleSearchCandidates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{
			name:  "plain title",
			query: "Attack on Titan",
			want:  []string{"Attack on Titan"},
		},
		{
			name:  "title containing a dot",
			query: "Dr. Stone",
			want:  []string{"Dr Stone"},
		},
		{
			name:  "season suffix",
			query: "Attack on Titan Season 2",
			want:  []string{"Attack on Titan", "Attack on Titan Season 2"},
		},
		{
			name:  "path with generic season folder and release noise",
			query: "/anime/Made in Abyss (2017)/Season 2/[Group] Made.in.Abyss.S02E03.1080p.WEB-DL.mkv",
			want:  []string{"Made in Abyss"},
		},
		{
			name:  "cour suffix",
			query: "Mushoku Tensei Part 2",
			want:  []string{"Mushoku Tensei", "Mushoku Tensei Part 2"},
		},
		{
			name:  "windows path",
			query: `D:\Anime\Frieren (2023)\Season 1\Frieren - E04.mkv`,
			want:  []string{"Frieren"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := titleSearchCandidates(tt.query); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("titleSearchCandidates(%q) = %#v, want %#v", tt.query, got, tt.want)
			}
		})
	}
}

func TestBetterSeriesHit(t *testing.T) {
	t.Parallel()

	exact := shokoSeriesSearchResult{ExactMatch: true, Distance: 5}
	fuzzy := shokoSeriesSearchResult{Distance: 0.1}
	if !betterSeriesHit(exact, 2, 0.8, fuzzy, 0, 1) {
		t.Fatal("exact match should rank ahead of fuzzy match")
	}

	a := shokoSeriesSearchResult{Distance: 0.5}
	b := shokoSeriesSearchResult{Distance: 0.1}
	if !betterSeriesHit(a, 1, 0.95, b, 0, 0.7) {
		t.Fatal("stronger local title similarity should rank ahead of Shoko distance")
	}
}

func TestAppendSearchAliasDeduplicatesNormalizedTitles(t *testing.T) {
	t.Parallel()

	aliases := appendSearchAlias(nil, "Dr. Stone", "en", "alternate")
	aliases = appendSearchAlias(aliases, "dr stone", "", "alternate")
	aliases = appendSearchAlias(aliases, "ドクターストーン", "ja", "localized")
	if len(aliases) != 2 {
		t.Fatalf("len(aliases) = %d, want 2: %#v", len(aliases), aliases)
	}
}
