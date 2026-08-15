package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestSearchPreservesExactSeriesInsteadOfCollapsingToGroup(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/Series/Search":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"ExactMatch": true,
					"Distance":   0,
					"Match":      "night shift nurses experiment",
					"Name":       "Night Shift Nurses - Experiment",
					"IDs": map[string]any{
						"ID": 158, "AniDB": 3415, "ParentGroup": 876, "TopLevelGroup": 876,
					},
				},
			})
		case "/api/v3/Series/158":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Name":    "Night Shift Nurses - Experiment",
				"AirDate": "2004-07-30",
				"Type":    "OVA",
				"IDs": map[string]any{
					"ID": 158, "AniDB": 3415, "ParentGroup": 876, "TopLevelGroup": 876,
					"TMDB": map[string]any{"Show": []int{98026}},
				},
			})
		case "/api/v3/Group/876/Series":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"Name": "Night Shift Nurses", "IDs": map[string]any{"ID": 190}},
				{
					"Name": "Night Shift Nurses - Experiment",
					"IDs":  map[string]any{"ID": 158, "ParentGroup": 876, "TopLevelGroup": 876},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	p := NewProvider(server.URL, "test-key", TagFilterConfig{})
	results, err := p.Search(context.Background(), "Night Shift Nurses - Experiment", "series", 0, nil, "en")
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	got := results[0]
	if got.Name != "Night Shift Nurses - Experiment" || got.ItemType != "series" {
		t.Fatalf("result = (%q, %q), want exact series", got.Name, got.ItemType)
	}
	if got.ProviderIDs["shoko"] != "158" || got.ProviderIDs["shoko_series"] != "158" {
		t.Fatalf("provider IDs = %#v, want Shoko series 158", got.ProviderIDs)
	}
	if got.ProviderIDs["shoko_group"] != "" {
		t.Fatalf("provider IDs = %#v, exact series must not resolve as a group", got.ProviderIDs)
	}
	if got.ProviderIDs["tmdb"] != "98026" {
		t.Fatalf("TMDB ID = %q, want 98026", got.ProviderIDs["tmdb"])
	}

	results, err = p.Search(context.Background(), "Night Shift Nurses - Experiment", "series", 0,
		map[string]string{"shoko": "876", "shoko_group": "876"}, "en")
	if err != nil {
		t.Fatalf("Search() with existing group error = %v", err)
	}
	if len(results) != 1 || results[0].ProviderIDs["shoko_series"] != "158" {
		t.Fatalf("Search() with existing group = %#v, want exact Shoko series 158", results)
	}
}

func TestExactLinkedSeriesSearchResultsPreservesTMDBSeries(t *testing.T) {
	t.Parallel()

	series := &shokoSeries{Name: "Night Shift Nurses - Experiment", Type: "OVA"}
	series.IDs.ID = 158
	series.IDs.AniDB = 3415
	series.IDs.ParentGroup = 876
	series.IDs.TopLevelGroup = 876
	series.IDs.TMDB.Show = []int{98026}

	p := &Provider{}
	results := p.exactLinkedSeriesSearchResults("Night Shift Nurses - Experiment", []*shokoSeries{series})
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	got := results[0]
	if got.ProviderIDs["shoko_series"] != "158" || got.ProviderIDs["shoko_group"] != "" {
		t.Fatalf("provider IDs = %#v, want exact Shoko series 158", got.ProviderIDs)
	}
	if got.ProviderIDs["tmdb"] != "98026" || got.ItemType != "series" {
		t.Fatalf("result = %#v, want TMDB show 98026 as a series", got)
	}
}
