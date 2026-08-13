package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/time/rate"
)

// ============================================================================
// TAG FILTER CONFIG
// ============================================================================

type TagFilterConfig struct {
	ShowVerifiedTags   bool
	HideSpoilerTags    bool
	TagWeightThreshold int
}

// ============================================================================
// SHOKO CLIENT
// ============================================================================

const (
	// Shoko is normally on the local network, so keep interactive requests
	// responsive while preventing expanded searches from becoming an
	// unbounded burst. One limiter is shared by all calls from this client.
	shokoRequestsPerSecond = 10
	shokoRequestBurst      = 3
)

type shokoClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
	limiter *rate.Limiter
	config  TagFilterConfig

	cacheMu     sync.RWMutex
	cache       map[int]*shokoSeries
	groupCache  map[int]*shokoGroup
	groupSeries map[int][]shokoSeries

	seriesListMu      sync.RWMutex
	seriesList        []shokoSeries
	seriesListFetched time.Time

	groupListMu      sync.RWMutex
	groupList        []shokoGroup
	groupListFetched time.Time
}

func newShokoClient(baseURL, apiKey string, config TagFilterConfig) *shokoClient {
	return &shokoClient{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		apiKey:  strings.TrimSpace(apiKey),
		config:  config,
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
		limiter:     rate.NewLimiter(rate.Limit(shokoRequestsPerSecond), shokoRequestBurst),
		cache:       make(map[int]*shokoSeries),
		groupCache:  make(map[int]*shokoGroup),
		groupSeries: make(map[int][]shokoSeries),
	}
}

func (c *shokoClient) doGet(ctx context.Context, endpoint string) (*http.Response, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("wait for Shoko request limit: %w", err)
	}

	u, err := url.Parse(c.baseURL + endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse Shoko URL: %w", err)
	}
	if c.apiKey != "" {
		query := u.Query()
		query.Set("apikey", c.apiKey)
		u.RawQuery = query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Silo-Shoko-Plugin/0.2.4")
	return c.http.Do(req)
}

// ============================================================================
// RESPONSE TYPES
// ============================================================================

type shokoSeriesListResponse struct {
	Total int           `json:"Total"`
	List  []shokoSeries `json:"List"`
}

type shokoGroupListResponse struct {
	Total int          `json:"Total"`
	List  []shokoGroup `json:"List"`
}

type shokoSeriesSearchResult struct {
	ExactMatch       bool    `json:"ExactMatch"`
	Index            int     `json:"Index"`
	Distance         float64 `json:"Distance"`
	LengthDifference int     `json:"LengthDifference"`
	Match            string  `json:"Match"`
	IDs              struct {
		ParentGroup   int          `json:"ParentGroup"`
		TopLevelGroup int          `json:"TopLevelGroup"`
		AniDB         int          `json:"AniDB"`
		TMDB          shokoTMDBIDs `json:"TMDB"`
		TVDB          interface{}  `json:"TvDB"`
		IMDB          interface{}  `json:"IMDB"`
		MAL           []int        `json:"MAL"`
		ID            int          `json:"ID"`
	} `json:"IDs"`
	Name        string `json:"Name"`
	Description string `json:"Description"`
	Images      struct {
		Posters   []shokoImage `json:"Posters"`
		Backdrops []shokoImage `json:"Backdrops"`
		Banners   []shokoImage `json:"Banners"`
		Logos     []shokoImage `json:"Logos"`
	} `json:"Images"`
}

type shokoGroup struct {
	IDs struct {
		PreferredSeries *int `json:"PreferredSeries"`
		MainSeries      int  `json:"MainSeries"`
		MainAnime       int  `json:"MainAnime"`
		ParentGroup     *int `json:"ParentGroup"`
		TopLevelGroup   int  `json:"TopLevelGroup"`
		ID              int  `json:"ID"`
	} `json:"IDs"`
	SortName    string `json:"SortName"`
	Description string `json:"Description"`
	Name        string `json:"Name"`
	Size        int    `json:"Size"`
	Images      struct {
		Posters   []shokoImage `json:"Posters"`
		Backdrops []shokoImage `json:"Backdrops"`
		Banners   []shokoImage `json:"Banners"`
		Logos     []shokoImage `json:"Logos"`
	} `json:"Images"`
}

type shokoSeries struct {
	IDs struct {
		ParentGroup   int          `json:"ParentGroup"`
		TopLevelGroup int          `json:"TopLevelGroup"`
		AniDB         int          `json:"AniDB"`
		TMDB          shokoTMDBIDs `json:"TMDB"`
		TVDB          interface{}  `json:"TvDB"`
		IMDB          interface{}  `json:"IMDB"`
		MAL           []int        `json:"MAL"`
		ID            int          `json:"ID"`
	} `json:"IDs"`
	HasCustomName bool        `json:"HasCustomName"`
	Name          string      `json:"Name"`
	Description   string      `json:"Description"`
	Type          string      `json:"Type"`
	AirDate       string      `json:"AirDate"`
	EndDate       string      `json:"EndDate"`
	EpisodeCount  int         `json:"EpisodeCount"`
	AirsOn        interface{} `json:"AirsOn"`
	YearlySeasons interface{} `json:"YearlySeasons"`
	UserRating    interface{} `json:"UserRating"`
	Images        struct {
		Posters   []shokoImage `json:"Posters"`
		Backdrops []shokoImage `json:"Backdrops"`
		Banners   []shokoImage `json:"Banners"`
		Logos     []shokoImage `json:"Logos"`
	} `json:"Images"`
	Tags []shokoTag `json:"Tags"`

	AniDB *shokoAniDBInfo `json:"AniDB,omitempty"`
}

type shokoImagesResponse struct {
	Posters   []shokoImage `json:"Posters"`
	Backdrops []shokoImage `json:"Backdrops"`
	Banners   []shokoImage `json:"Banners"`
	Logos     []shokoImage `json:"Logos"`
}

type shokoTMDBIDs struct {
	Episode []int `json:"Episode"`
	Movie   []int `json:"Movie"`
	Show    []int `json:"Show"`
}

type shokoImage struct {
	ID               int     `json:"ID"`
	Type             string  `json:"Type"`
	Source           string  `json:"Source"`
	LanguageCode     *string `json:"LanguageCode"`
	RelativeFilepath string  `json:"RelativeFilepath"`
	Preferred        bool    `json:"Preferred"`
	Disabled         bool    `json:"Disabled"`
	Width            int     `json:"Width"`
	Height           int     `json:"Height"`
}

type shokoTag struct {
	ID          int    `json:"ID"`
	ParentID    int    `json:"ParentID"`
	Name        string `json:"Name"`
	Description string `json:"Description"`
	IsVerified  bool   `json:"IsVerified"`
	IsSpoiler   bool   `json:"IsSpoiler"`
	Weight      int    `json:"Weight"`
	Source      string `json:"Source"`
}

type shokoAniDBInfo struct {
	ID           int               `json:"ID"`
	ShokoID      int               `json:"ShokoID"`
	Type         string            `json:"Type"`
	Title        string            `json:"Title"`
	Titles       []shokoAniDBTitle `json:"Titles"`
	Description  string            `json:"Description"`
	AirDate      string            `json:"AirDate"`
	EndDate      string            `json:"EndDate"`
	Restricted   bool              `json:"Restricted"`
	Poster       *shokoImage       `json:"Poster"`
	EpisodeCount int               `json:"EpisodeCount"`
	Rating       *shokoRating      `json:"Rating"`
}

type shokoAniDBTitle struct {
	Name      string `json:"Name"`
	Language  string `json:"Language"`
	Type      string `json:"Type"`
	Default   bool   `json:"Default"`
	Preferred bool   `json:"Preferred"`
	Source    string `json:"Source"`
}

type shokoRating struct {
	Value    float64 `json:"Value"`
	MaxValue float64 `json:"MaxValue"`
	Source   string  `json:"Source"`
	Votes    int     `json:"Votes"`
	Type     *string `json:"Type"`
}

type shokoEpisodeRoot struct {
	Total int                `json:"Total"`
	List  []shokoFullEpisode `json:"List"`
}

type shokoAniDBEpisode struct {
	ID            int               `json:"ID"`
	AnimeID       int               `json:"AnimeID"`
	Type          string            `json:"Type"`
	EpisodeNumber int               `json:"EpisodeNumber"`
	AirDate       string            `json:"AirDate"`
	Title         string            `json:"Title"`
	Titles        []shokoAniDBTitle `json:"Titles"`
	Description   string            `json:"Description"`
}

type shokoFullEpisode struct {
	IDs struct {
		ParentSeries int          `json:"ParentSeries"`
		AniDB        int          `json:"AniDB"`
		ID           int          `json:"ID"`
		TMDB         shokoTMDBIDs `json:"TMDB"`
		TVDB         interface{}  `json:"TvDB"`
	} `json:"IDs"`
	HasCustomName bool   `json:"HasCustomName"`
	Name          string `json:"Name"`
	Description   string `json:"Description"`
	Duration      string `json:"Duration"`
	Images        struct {
		Posters    []shokoImage `json:"Posters"`
		Thumbnails []shokoImage `json:"Thumbnails"`
	} `json:"Images"`
	AniDB   *shokoAniDBEpisode `json:"AniDB,omitempty"`
	Created string             `json:"Created"`
	Updated string             `json:"Updated"`
	Size    int                `json:"Size"`
}

// ============================================================================
// SANITIZATION & FORMATTING
// ============================================================================

func sanitizeDescription(desc string) string {
	re := regexp.MustCompile(`\[([^\]]+)\]`)
	desc = re.ReplaceAllString(desc, "$1")

	re = regexp.MustCompile(`https?://[^\s\[\]]+\s*`)
	desc = re.ReplaceAllString(desc, "")

	re = regexp.MustCompile(`\s{2,}`)
	return strings.TrimSpace(re.ReplaceAllString(desc, " "))
}

func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(string(s[0])) + s[1:]
}

func buildGenresFromTags(tags []shokoTag) []string {
	genres := make([]string, 0, len(tags))
	for _, tag := range tags {
		genres = append(genres, capitalizeFirst(tag.Name))
	}
	sort.Strings(genres)
	return genres
}

func imageLanguage(img shokoImage) string {
	if img.LanguageCode == nil {
		return ""
	}
	return *img.LanguageCode
}

func parseDuration(durationStr string) int {
	if durationStr == "" {
		return 0
	}
	durPart := strings.Split(durationStr, ".")[0]
	parts := strings.Split(durPart, ":")
	if len(parts) != 3 {
		return 0
	}
	hours, _ := strconv.Atoi(parts[0])
	minutes, _ := strconv.Atoi(parts[1])
	seconds, _ := strconv.Atoi(parts[2])
	totalSeconds := hours*3600 + minutes*60 + seconds
	return totalSeconds / 60
}

func isMovieType(series *shokoSeries) bool {
	if series.Type == "OVA" || series.Type == "Movie" {
		return true
	}
	if series.AniDB != nil {
		if series.AniDB.Type == "OVA" || series.AniDB.Type == "Movie" {
			return true
		}
	}
	return false
}

// ============================================================================
// CORE API METHODS
// ============================================================================

const seriesListCacheTTL = 2 * time.Minute

func (c *shokoClient) getAllSeries(ctx context.Context) ([]shokoSeries, error) {
	c.seriesListMu.RLock()
	if len(c.seriesList) > 0 && time.Since(c.seriesListFetched) < seriesListCacheTTL {
		cached := append([]shokoSeries(nil), c.seriesList...)
		c.seriesListMu.RUnlock()
		return cached, nil
	}
	c.seriesListMu.RUnlock()

	var allSeries []shokoSeries
	page := 1

	for {
		endpoint := fmt.Sprintf("/api/v3/Series?page=%d", page)
		resp, err := c.doGet(ctx, endpoint)
		if err != nil {
			return nil, fmt.Errorf("getAllSeries HTTP error (page %d): %w", page, err)
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("getAllSeries page %d returned %d: %s", page, resp.StatusCode, string(body))
		}

		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var wrapper shokoSeriesListResponse
		if err := json.Unmarshal(bodyBytes, &wrapper); err != nil {
			return nil, fmt.Errorf("getAllSeries JSON unmarshal (page %d): %w", page, err)
		}

		allSeries = append(allSeries, wrapper.List...)
		fmt.Printf("[SHOKO][ALLSERIES] Page %d: got %d series (total so far: %d / %d)\n",
			page, len(wrapper.List), len(allSeries), wrapper.Total)

		if len(allSeries) >= wrapper.Total || len(wrapper.List) == 0 {
			break
		}
		page++
	}

	fmt.Printf("[SHOKO][ALLSERIES] Loaded %d series total across %d pages\n", len(allSeries), page)
	c.seriesListMu.Lock()
	c.seriesList = append(c.seriesList[:0], allSeries...)
	c.seriesListFetched = time.Now()
	c.seriesListMu.Unlock()
	return allSeries, nil
}

func (c *shokoClient) searchSeries(ctx context.Context, query string, limit int) ([]shokoSeriesSearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}

	values := url.Values{}
	values.Set("query", query)
	values.Set("fuzzy", "true")
	values.Set("limit", strconv.Itoa(limit))
	values.Add("includeDataFrom", "AniDB")
	endpoint := "/api/v3/Series/Search?" + values.Encode()

	resp, err := c.doGet(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("searchSeries HTTP error: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("searchSeries read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("searchSeries returned %d: %s", resp.StatusCode, string(body))
	}

	var results []shokoSeriesSearchResult
	if err := json.Unmarshal(body, &results); err != nil {
		return nil, fmt.Errorf("searchSeries JSON unmarshal: %w", err)
	}
	return results, nil
}

func (c *shokoClient) getAllGroups(ctx context.Context) ([]shokoGroup, error) {
	c.groupListMu.RLock()
	if len(c.groupList) > 0 && time.Since(c.groupListFetched) < seriesListCacheTTL {
		cached := append([]shokoGroup(nil), c.groupList...)
		c.groupListMu.RUnlock()
		return cached, nil
	}
	c.groupListMu.RUnlock()

	var allGroups []shokoGroup
	page := 1
	for {
		endpoint := fmt.Sprintf("/api/v3/Group?page=%d&pageSize=100", page)
		resp, err := c.doGet(ctx, endpoint)
		if err != nil {
			return nil, fmt.Errorf("getAllGroups HTTP error (page %d): %w", page, err)
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("getAllGroups page %d returned %d: %s", page, resp.StatusCode, string(body))
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("getAllGroups read page %d: %w", page, err)
		}

		var wrapper shokoGroupListResponse
		if err := json.Unmarshal(body, &wrapper); err != nil {
			var direct []shokoGroup
			if arrayErr := json.Unmarshal(body, &direct); arrayErr != nil {
				return nil, fmt.Errorf("getAllGroups JSON unmarshal (page %d): %w", page, err)
			}
			wrapper.List = direct
			wrapper.Total = len(direct)
		}
		allGroups = append(allGroups, wrapper.List...)
		if len(wrapper.List) == 0 || (wrapper.Total > 0 && len(allGroups) >= wrapper.Total) {
			break
		}
		page++
	}

	c.groupListMu.Lock()
	c.groupList = append(c.groupList[:0], allGroups...)
	c.groupListFetched = time.Now()
	c.groupListMu.Unlock()

	fmt.Printf("[SHOKO][ALLGROUPS] Loaded %d groups\n", len(allGroups))
	return allGroups, nil
}

func (c *shokoClient) getGroup(ctx context.Context, groupID int) (*shokoGroup, error) {
	c.cacheMu.RLock()
	cached, ok := c.groupCache[groupID]
	c.cacheMu.RUnlock()
	if ok {
		return cached, nil
	}

	endpoint := fmt.Sprintf("/api/v3/Group/%d", groupID)
	resp, err := c.doGet(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("getGroup HTTP error: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("getGroup returned %d: %s", resp.StatusCode, string(body))
	}

	var group shokoGroup
	if err := json.NewDecoder(resp.Body).Decode(&group); err != nil {
		return nil, fmt.Errorf("getGroup JSON unmarshal: %w", err)
	}
	if group.IDs.ID == 0 {
		return nil, fmt.Errorf("getGroup %d returned an empty group", groupID)
	}

	c.cacheMu.Lock()
	c.groupCache[groupID] = &group
	c.cacheMu.Unlock()
	return &group, nil
}

func (c *shokoClient) getGroupSeries(ctx context.Context, groupID int) ([]shokoSeries, error) {
	c.cacheMu.RLock()
	if cached, ok := c.groupSeries[groupID]; ok && len(cached) > 0 {
		copySeries := append([]shokoSeries(nil), cached...)
		c.cacheMu.RUnlock()
		return copySeries, nil
	}
	c.cacheMu.RUnlock()

	endpoint := fmt.Sprintf("/api/v3/Group/%d/Series", groupID)
	resp, err := c.doGet(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("getGroupSeries HTTP error: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("getGroupSeries returned %d: %s", resp.StatusCode, string(body))
	}

	var list []shokoSeries
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("getGroupSeries JSON unmarshal: %w", err)
	}
	c.cacheMu.Lock()
	c.groupSeries[groupID] = append([]shokoSeries(nil), list...)
	c.cacheMu.Unlock()
	return list, nil
}

func (c *shokoClient) getSeries(ctx context.Context, shokoID int) (*shokoSeries, error) {
	c.cacheMu.RLock()
	cached, ok := c.cache[shokoID]
	c.cacheMu.RUnlock()
	if ok {
		return cached, nil
	}

	endpoint := fmt.Sprintf("/api/v3/Series/%d?includeDataFrom=AniDB,TMDB", shokoID)

	resp, err := c.doGet(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("getSeries HTTP error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("getSeries returned %d: %s", resp.StatusCode, string(body))
	}

	bodyBytes, _ := io.ReadAll(resp.Body)

	var series shokoSeries
	if err := json.Unmarshal(bodyBytes, &series); err != nil {
		return nil, fmt.Errorf("getSeries JSON unmarshal: %w", err)
	}

	fmt.Printf("[SHOKO][GETSERIES] Got: %q (Shoko ID: %d, AniDB: %d, Type: %q)\n",
		series.Name, series.IDs.ID, series.IDs.AniDB, series.Type)

	if series.IDs.AniDB != 0 {
		if err := c.enrichWithAniDB(ctx, &series); err != nil {
			fmt.Printf("[SHOKO][GETSERIES] AniDB enrichment failed: %v\n", err)
		}
	}

	if err := c.enrichWithTags(ctx, &series); err != nil {
		fmt.Printf("[SHOKO][GETSERIES] Tags enrichment failed: %v\n", err)
	}

	// Store only after enrichment so callers never observe a partially populated series.
	c.cacheMu.Lock()
	if existing, ok := c.cache[shokoID]; ok {
		c.cacheMu.Unlock()
		return existing, nil
	}
	c.cache[shokoID] = &series
	c.cacheMu.Unlock()
	return &series, nil
}

func (c *shokoClient) enrichWithAniDB(ctx context.Context, series *shokoSeries) error {
	endpoint := fmt.Sprintf("/api/v3/Series/%d/AniDB", series.IDs.ID)
	resp, err := c.doGet(ctx, endpoint)
	if err != nil {
		return fmt.Errorf("AniDB sub-resource HTTP error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("AniDB sub-resource returned %d", resp.StatusCode)
	}

	var anidb shokoAniDBInfo
	if err := json.NewDecoder(resp.Body).Decode(&anidb); err != nil {
		return fmt.Errorf("AniDB sub-resource JSON unmarshal: %w", err)
	}

	series.AniDB = &anidb

	if series.AirDate == "" && anidb.AirDate != "" {
		series.AirDate = anidb.AirDate
	}
	if series.EndDate == "" && anidb.EndDate != "" {
		series.EndDate = anidb.EndDate
	}
	if series.Type == "" && anidb.Type != "" {
		series.Type = anidb.Type
	}
	if series.Description == "" && anidb.Description != "" {
		series.Description = anidb.Description
	}
	if series.EpisodeCount == 0 && anidb.EpisodeCount != 0 {
		series.EpisodeCount = anidb.EpisodeCount
	}

	fmt.Printf("[SHOKO][GETSERIES] AniDB enriched: Type=%q AirDate=%q EndDate=%q EpCount=%d\n",
		anidb.Type, anidb.AirDate, anidb.EndDate, anidb.EpisodeCount)

	return nil
}

func (c *shokoClient) enrichWithTags(ctx context.Context, series *shokoSeries) error {
	endpoint := fmt.Sprintf("/api/v3/Series/%d/Tags?includeRestricted=true", series.IDs.ID)
	resp, err := c.doGet(ctx, endpoint)
	if err != nil {
		return fmt.Errorf("Tags sub-resource HTTP error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Tags sub-resource returned %d", resp.StatusCode)
	}

	var tags []shokoTag
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return fmt.Errorf("Tags sub-resource JSON unmarshal: %w", err)
	}

	filtered := make([]shokoTag, 0, len(tags))
	for _, t := range tags {
		passed := true

		if c.config.ShowVerifiedTags && !t.IsVerified {
			passed = false
		}
		if c.config.HideSpoilerTags && t.IsSpoiler {
			passed = false
		}
		if t.Weight < c.config.TagWeightThreshold {
			passed = false
		}

		if passed {
			filtered = append(filtered, t)
		}
	}

	series.Tags = filtered
	fmt.Printf("[SHOKO][GETSERIES] Tags enriched: %d tags (filtered from %d, config: Verified=%v, Spoilers=%v, Weight>=%d)\n",
		len(filtered), len(tags), c.config.ShowVerifiedTags, c.config.HideSpoilerTags, c.config.TagWeightThreshold)
	return nil
}

func (c *shokoClient) getSeriesByAniDB(ctx context.Context, anidbID int) (*shokoSeries, error) {
	c.cacheMu.RLock()
	for _, s := range c.cache {
		if s.IDs.AniDB == anidbID {
			c.cacheMu.RUnlock()
			return s, nil
		}
	}
	c.cacheMu.RUnlock()

	endpoint := fmt.Sprintf("/api/v3/Series/AniDB/%d?includeDataFrom=AniDB,TMDB", anidbID)

	resp, err := c.doGet(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("getSeriesByAniDB HTTP error: %w", err)
	}

	if resp.StatusCode == http.StatusOK {
		defer resp.Body.Close()
		bodyBytes, _ := io.ReadAll(resp.Body)

		var series shokoSeries
		if err := json.Unmarshal(bodyBytes, &series); err != nil {
			return nil, fmt.Errorf("getSeriesByAniDB JSON unmarshal: %w", err)
		}

		if series.IDs.ID != 0 {
			if err := c.enrichWithAniDB(ctx, &series); err != nil {
				fmt.Printf("[SHOKO][GETSERIESBYANIDB] AniDB enrichment failed: %v\n", err)
			}
			if err := c.enrichWithTags(ctx, &series); err != nil {
				fmt.Printf("[SHOKO][GETSERIESBYANIDB] Tags enrichment failed: %v\n", err)
			}
			c.cacheMu.Lock()
			if existing, ok := c.cache[series.IDs.ID]; ok {
				c.cacheMu.Unlock()
				return existing, nil
			}
			c.cache[series.IDs.ID] = &series
			c.cacheMu.Unlock()
			return &series, nil
		}
	}
	resp.Body.Close()

	allSeries, err := c.getAllSeries(ctx)
	if err != nil {
		return nil, err
	}

	for _, s := range allSeries {
		if s.IDs.AniDB == anidbID {
			return c.getSeries(ctx, s.IDs.ID)
		}
	}

	return nil, fmt.Errorf("series with AniDB ID %d not found", anidbID)
}

func (c *shokoClient) getSeriesByTMDBShow(ctx context.Context, tmdbID int) ([]*shokoSeries, error) {
	if tmdbID <= 0 {
		return nil, fmt.Errorf("invalid TMDB show ID %d", tmdbID)
	}

	// Check enriched cache first.
	c.cacheMu.RLock()
	cachedMatches := make([]int, 0)
	for id, series := range c.cache {
		for _, linked := range series.IDs.TMDB.Show {
			if linked == tmdbID {
				cachedMatches = append(cachedMatches, id)
				break
			}
		}
	}
	c.cacheMu.RUnlock()

	seen := map[int]struct{}{}
	results := make([]*shokoSeries, 0)
	for _, id := range cachedMatches {
		full, err := c.getSeries(ctx, id)
		if err == nil {
			seen[id] = struct{}{}
			results = append(results, full)
		}
	}

	// The Series list includes external IDs on current Shoko versions. This also
	// handles multiple AniDB/Shoko series linked to one TMDB show (seasons/cours).
	allSeries, err := c.getAllSeries(ctx)
	if err != nil {
		if len(results) > 0 {
			return results, nil
		}
		return nil, err
	}
	for _, series := range allSeries {
		if _, ok := seen[series.IDs.ID]; ok {
			continue
		}
		matched := false
		for _, linked := range series.IDs.TMDB.Show {
			if linked == tmdbID {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		full, err := c.getSeries(ctx, series.IDs.ID)
		if err != nil {
			continue
		}
		seen[series.IDs.ID] = struct{}{}
		results = append(results, full)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no Shoko series linked to TMDB show %d", tmdbID)
	}
	return results, nil
}

// ============================================================================
// RESULT TYPES
// ============================================================================

type SearchResult struct {
	Name          string
	OriginalTitle string
	TitleAliases  []SearchTitleAlias
	Year          int
	Overview      string
	ProviderIDs   map[string]string
	ImageURL      string
	ItemType      string
}

type SearchTitleAlias struct {
	Title    string
	Language string
	Kind     string
}

type rankedSeriesHit struct {
	hit            shokoSeriesSearchResult
	candidateIndex int
	titleScore     float64
}

type MetadataResult struct {
	Title         string
	OriginalTitle string
	Year          int
	Overview      string
	Runtime       int
	Genres        []string
	Studios       []string
	ProviderIDs   map[string]string
	PosterPath    string
	BackdropPath  string
	SeasonCount   int
	FirstAirDate  string
	LastAirDate   string
	ReleaseDate   string
	ShowStatus    string
	ItemType      string
}

type SeasonResult struct {
	ContentID    string
	SeasonNumber int
	Title        string
	Overview     string
	AirDate      string
	PosterPath   string
	ProviderIDs  map[string]string
}

type EpisodeResult struct {
	ContentID     string
	SeasonNumber  int
	EpisodeNumber int
	Title         string
	Overview      string
	AirDate       string
	Runtime       int
	StillPath     string
	ProviderIDs   map[string]string
}

type ImageResult struct {
	Kind     string
	URL      string
	Language string
	Width    int
	Height   int
}

// ============================================================================
// PROVIDER IMPLEMENTATION
// ============================================================================

type Provider struct {
	client *shokoClient
	config TagFilterConfig
}

func NewProvider(baseURL, apiKey string, config TagFilterConfig) *Provider {
	fmt.Printf("[SHOKO][INIT] Creating provider: baseURL=%s, ShowVerified=%v, HideSpoilers=%v, WeightThresh=%d\n",
		baseURL, config.ShowVerifiedTags, config.HideSpoilerTags, config.TagWeightThreshold)
	return &Provider{
		client: newShokoClient(baseURL, apiKey, config),
		config: config,
	}
}

func (p *Provider) Slug() string       { return "shoko" }
func (p *Provider) Name() string       { return "Shoko Anime Metadata" }
func (p *Provider) ForTypes() []string { return []string{"series", "season", "episode", "movie"} }

// ============================================================================
// IMAGE URL HELPERS
// ============================================================================

func (p *Provider) imageURL(source, imageType, imageID string) string {
	u, err := url.Parse(p.client.baseURL + "/api/v3/Image/" +
		url.PathEscape(source) + "/" + url.PathEscape(imageType) + "/" + url.PathEscape(imageID))
	if err != nil {
		return ""
	}
	if p.client.apiKey != "" {
		q := u.Query()
		q.Set("apikey", p.client.apiKey)
		u.RawQuery = q.Encode()
	}
	return u.String()
}

func (p *Provider) buildImageURL(img shokoImage, fallbackType string) string {
	if img.ID == 0 || img.Source == "" {
		return ""
	}
	imgType := img.Type
	if imgType == "" {
		imgType = fallbackType
	}
	return p.imageURL(img.Source, imgType, strconv.Itoa(img.ID))
}

func (p *Provider) ResolveImageURL(path string) string {
	if path == "" {
		return ""
	}

	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}

	if strings.HasPrefix(path, "shoko://") {
		barePath := strings.TrimPrefix(path, "shoko://")
		parts := strings.Split(barePath, "/")
		if len(parts) == 3 {
			fullURL := p.imageURL(parts[0], parts[1], parts[2])
			fmt.Printf("[SHOKO][RESOLVE] Resolved canonical image path %s\n", path)
			return fullURL
		}
	}

	parts := strings.Split(path, "/")
	if len(parts) == 2 {
		seriesID := parts[0]
		imageType := strings.ToLower(parts[1])

		shokoType := "Posters"
		fallback := "Poster"
		if imageType == "backdrop" || imageType == "backdrops" {
			shokoType = "Backdrops"
			fallback = "Backdrop"
		} else if imageType == "banner" || imageType == "banners" {
			shokoType = "Banners"
			fallback = "Banner"
		} else if imageType == "logo" || imageType == "logos" {
			shokoType = "Logos"
			fallback = "Logo"
		}

		resolvedURL := p.resolveSeriesImageByID(seriesID, shokoType, fallback)
		if resolvedURL != "" {
			fmt.Printf("[SHOKO][RESOLVE] %s -> resolved via live lookup\n", path)
			return resolvedURL
		}

		fmt.Printf("[SHOKO][RESOLVE] %s -> no images found for type %s\n", path, shokoType)
		return ""
	}

	if len(parts) == 3 {
		fullURL := p.imageURL(parts[0], parts[1], parts[2])
		fmt.Printf("[SHOKO][RESOLVE] Resolved bare image path %s\n", path)
		return fullURL
	}

	fmt.Printf("[SHOKO][RESOLVE] Unrecognized path format: %s\n", path)
	return ""
}

func (p *Provider) resolveSeriesImageByID(seriesID, imageType, fallbackType string) string {
	endpoint := fmt.Sprintf("/api/v3/Series/%s/Images", seriesID)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := p.client.doGet(ctx, endpoint)
	if err != nil {
		fmt.Printf("[SHOKO][RESOLVE] Failed to fetch images for series %s: %v\n", seriesID, err)
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Printf("[SHOKO][RESOLVE] Series %s images returned %d\n", seriesID, resp.StatusCode)
		return ""
	}

	var imgResp shokoImagesResponse
	if err := json.NewDecoder(resp.Body).Decode(&imgResp); err != nil {
		fmt.Printf("[SHOKO][RESOLVE] Failed to parse images for series %s: %v\n", seriesID, err)
		return ""
	}

	var images []shokoImage
	switch imageType {
	case "Posters":
		images = imgResp.Posters
	case "Backdrops":
		images = imgResp.Backdrops
	case "Banners":
		images = imgResp.Banners
	case "Logos":
		images = imgResp.Logos
	}

	if len(images) == 0 {
		return ""
	}

	return p.buildImageURL(images[0], fallbackType)
}

// ============================================================================
// FUZZY MATCHING
// ============================================================================

func extractSeriesNameFromFile(filePath string) string {
	filePath = filepath.ToSlash(filepath.Clean(filePath))
	parts := strings.Split(filePath, "/")
	name := parts[len(parts)-1]
	if len(parts) > 1 {
		dirname := parts[len(parts)-2]
		if !strings.ContainsRune(dirname, '.') {
			name = dirname
		}
	}

	noises := []string{
		` \[\d{4}\]`, ` \[\d+[pP]\]`, ` - E\d+`, ` - \d+`,
		` Season \d+`, ` Part \d+`, ` \([^)]+\)$`, `\.[^\.]+$`,
	}
	for _, pattern := range noises {
		if re, err := regexp.Compile(pattern); err == nil {
			name = re.ReplaceAllString(name, "")
		}
	}
	name = regexp.MustCompile(`[-_\[\]()]+`).ReplaceAllString(name, " ")
	return strings.TrimSpace(name)
}

var (
	seasonSuffixPattern  = regexp.MustCompile(`(?i)\s+(?:season|series|cour|part)\s*[-_. ]*\d+\s*$`)
	episodeSuffixPattern = regexp.MustCompile(`(?i)\s+(?:s\d{1,2}\s*)?(?:e|ep|episode)\s*\d+(?:\s*[-+]\s*\d+)?\s*$`)
	yearSuffixPattern    = regexp.MustCompile(`\s*[\[(](?:19|20)\d{2}[\])]?\s*$`)
	bracketNoisePattern  = regexp.MustCompile(`\s*[\[(][^\])]*[\])]`)
	releaseNoisePattern  = regexp.MustCompile(`(?i)\b(?:2160p|1080p|720p|480p|web[-_. ]?dl|webrip|blu[-_. ]?ray|bdrip|x26[45]|h\.?26[45]|hevc|av1|aac|flac|multi(?:sub)?|dual[-_. ]?audio)\b.*$`)
)

// titleSearchCandidates turns the value supplied by Silo into a small set of
// useful Shoko queries. Silo normally sends a parsed title, but some scanners
// pass a filename or path-like title when parsing fails.
func titleSearchCandidates(query string) []string {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}

	pathQuery := strings.ReplaceAll(filepath.ToSlash(query), `\`, "/")
	inputs := []string{query}
	if strings.Contains(pathQuery, "/") {
		inputs = nil
		parts := strings.Split(pathQuery, "/")
		for i := len(parts) - 1; i >= 0 && len(inputs) < 5; i-- {
			part := strings.TrimSpace(parts[i])
			if part != "" && !isWindowsDrive(part) && !isGenericSeasonFolder(part) && !isGenericLibraryFolder(part) {
				inputs = append(inputs, part)
			}
		}
	}

	seen := make(map[string]struct{})
	result := make([]string, 0, 6)
	add := func(value string) {
		value = strings.TrimSpace(value)
		key := normalizeTitle(value)
		if key == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}

	for _, input := range inputs {
		base := stripMediaExtension(input)
		base = strings.TrimSpace(strings.NewReplacer("_", " ", ".", " ").Replace(base))
		cleaned := bracketNoisePattern.ReplaceAllString(base, " ")
		cleaned = releaseNoisePattern.ReplaceAllString(cleaned, "")
		cleaned = episodeSuffixPattern.ReplaceAllString(cleaned, "")
		cleaned = strings.TrimRight(cleaned, " -_.")
		cleaned = strings.Join(strings.Fields(cleaned), " ")
		withoutSeason := seasonSuffixPattern.ReplaceAllString(cleaned, "")
		withoutYear := yearSuffixPattern.ReplaceAllString(withoutSeason, "")

		// Prefer cleaned titles, while retaining the original parsed title as a
		// fallback for legitimate titles containing words such as "Part".
		add(withoutYear)
		add(withoutSeason)
		add(cleaned)
		if !strings.Contains(pathQuery, "/") {
			add(query)
		}
		if len(result) >= 6 {
			break
		}
	}
	return result
}

func isWindowsDrive(value string) bool {
	return len(value) == 2 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':'
}

func isGenericLibraryFolder(value string) bool {
	switch normalizeTitle(value) {
	case "anime", "tv", "tv shows", "shows", "series", "media", "videos":
		return true
	default:
		return false
	}
}

func isGenericSeasonFolder(value string) bool {
	normalized := normalizeTitle(stripMediaExtension(value))
	fields := strings.Fields(normalized)
	if len(fields) != 2 {
		return false
	}
	if fields[0] != "season" && fields[0] != "series" && fields[0] != "cour" && fields[0] != "part" {
		return false
	}
	_, err := strconv.Atoi(fields[1])
	return err == nil
}

func stripMediaExtension(value string) string {
	ext := strings.ToLower(filepath.Ext(value))
	switch ext {
	case ".mkv", ".mp4", ".avi", ".mov", ".m4v", ".wmv", ".ts", ".m2ts", ".webm", ".ogm", ".iso":
		return strings.TrimSuffix(value, filepath.Ext(value))
	default:
		return value
	}
}

var titleStopWords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "at": {}, "by": {}, "for": {}, "from": {},
	"in": {}, "of": {}, "on": {}, "or": {}, "the": {}, "to": {}, "with": {},
}

func normalizeTitle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	b.Grow(len(s))
	lastSpace := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastSpace = false
		case r >= 0x80 && (unicode.IsLetter(r) || unicode.IsNumber(r)):
			b.WriteRune(r)
			lastSpace = false
		default:
			if !lastSpace && b.Len() > 0 {
				b.WriteByte(' ')
				lastSpace = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func meaningfulTitleTokens(s string) []string {
	fields := strings.Fields(normalizeTitle(s))
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		if _, ignored := titleStopWords[field]; ignored {
			continue
		}
		result = append(result, field)
	}
	return result
}

func fuzzyMatchScore(candidate, query string) float64 {
	candidateNorm := normalizeTitle(candidate)
	queryNorm := normalizeTitle(query)
	if candidateNorm == "" || queryNorm == "" {
		return 0
	}
	if candidateNorm == queryNorm {
		return 1.0
	}
	if strings.Contains(candidateNorm, queryNorm) || strings.Contains(queryNorm, candidateNorm) {
		return 0.92
	}

	candidateTokens := meaningfulTitleTokens(candidateNorm)
	queryTokens := meaningfulTitleTokens(queryNorm)
	if len(candidateTokens) == 0 || len(queryTokens) == 0 {
		return 0
	}

	used := make([]bool, len(candidateTokens))
	matched := 0
	for _, queryToken := range queryTokens {
		for i, candidateToken := range candidateTokens {
			if used[i] || candidateToken != queryToken {
				continue
			}
			used[i] = true
			matched++
			break
		}
	}
	if matched == 0 {
		return 0
	}

	queryCoverage := float64(matched) / float64(len(queryTokens))
	candidateCoverage := float64(matched) / float64(len(candidateTokens))
	return queryCoverage*0.70 + candidateCoverage*0.30
}

func extractYear(dateStr string) int {
	if len(dateStr) < 4 {
		return 0
	}
	y, err := strconv.Atoi(dateStr[:4])
	if err != nil {
		return 0
	}
	return y
}

// ============================================================================
// SEARCH
// ============================================================================

func firstImage(images []shokoImage) *shokoImage {
	for i := range images {
		if images[i].Disabled || images[i].ID == 0 || images[i].Source == "" {
			continue
		}
		return &images[i]
	}
	return nil
}

func isSeasonSeries(series *shokoSeries) bool {
	seriesType := strings.TrimSpace(series.Type)
	if series.AniDB != nil && series.AniDB.Type != "" {
		seriesType = series.AniDB.Type
	}
	switch strings.ToLower(seriesType) {
	case "tv", "web", "tvspecial", "tv special":
		return true
	default:
		return false
	}
}

func earliestDate(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" || a <= b {
		return a
	}
	return b
}

func latestDate(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" || a >= b {
		return a
	}
	return b
}

func providerInt(providerID string, providerIDs map[string]string, keys ...string) (int, bool) {
	for _, key := range keys {
		if raw := strings.TrimSpace(providerIDs[key]); raw != "" {
			id, err := strconv.Atoi(raw)
			if err == nil && id > 0 {
				return id, true
			}
		}
	}
	if raw := strings.TrimSpace(providerID); raw != "" {
		id, err := strconv.Atoi(raw)
		if err == nil && id > 0 {
			return id, true
		}
	}
	return 0, false
}

func providerTMDBShowID(providerIDs map[string]string) (int, bool) {
	for key, raw := range providerIDs {
		k := strings.ToLower(strings.TrimSpace(key))
		switch k {
		case "tmdb", "tmdb_show", "tmdb_tv", "tmdbshow", "tmdbid":
			if id, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && id > 0 {
				return id, true
			}
		}
	}
	return 0, false
}

func (p *Provider) seasonSeriesForGroup(ctx context.Context, groupID int) ([]*shokoSeries, error) {
	children, err := p.client.getGroupSeries(ctx, groupID)
	if err != nil {
		return nil, err
	}
	seasons := make([]*shokoSeries, 0, len(children))
	for _, child := range children {
		if child.IDs.ID == 0 {
			continue
		}
		full, err := p.client.getSeries(ctx, child.IDs.ID)
		if err != nil {
			fmt.Printf("[SHOKO][GROUP] Could not enrich child series %d: %v\n", child.IDs.ID, err)
			continue
		}
		if !isSeasonSeries(full) {
			fmt.Printf("[SHOKO][GROUP] Excluding non-season child %d %q type=%q\n", full.IDs.ID, full.Name, full.Type)
			continue
		}
		seasons = append(seasons, full)
	}

	sort.SliceStable(seasons, func(i, j int) bool {
		a, b := seasons[i], seasons[j]
		ad, bd := a.AirDate, b.AirDate
		if a.AniDB != nil && a.AniDB.AirDate != "" {
			ad = a.AniDB.AirDate
		}
		if b.AniDB != nil && b.AniDB.AirDate != "" {
			bd = b.AniDB.AirDate
		}
		if ad != bd {
			if ad == "" {
				return false
			}
			if bd == "" {
				return true
			}
			return ad < bd
		}
		if a.IDs.AniDB != b.IDs.AniDB {
			return a.IDs.AniDB < b.IDs.AniDB
		}
		return a.IDs.ID < b.IDs.ID
	})
	return seasons, nil
}

func (p *Provider) Search(ctx context.Context, query, itemType string, year int32, providerIDs map[string]string, language string) ([]SearchResult, error) {
	fmt.Printf("[SHOKO][SEARCH] Query=%q ItemType=%q Year=%d ProviderIDs=%+v\n", query, itemType, year, providerIDs)

	if strings.EqualFold(itemType, "movie") {
		return p.searchMovieSeries(ctx, query, providerIDs)
	}

	if groupID, ok := providerInt("", providerIDs, "shoko_group"); ok {
		group, err := p.client.getGroup(ctx, groupID)
		if err != nil {
			return nil, err
		}
		return []SearchResult{p.groupSearchResult(ctx, group)}, nil
	}

	if tmdbID, ok := providerTMDBShowID(providerIDs); ok {
		seriesMatches, matchErr := p.client.getSeriesByTMDBShow(ctx, tmdbID)
		if matchErr == nil && len(seriesMatches) > 0 {
			seenGroups := map[int]struct{}{}
			results := make([]SearchResult, 0)
			for _, series := range seriesMatches {
				groupID := series.IDs.TopLevelGroup
				if groupID == 0 {
					groupID = series.IDs.ParentGroup
				}
				if groupID == 0 {
					continue
				}
				if _, seen := seenGroups[groupID]; seen {
					continue
				}
				group, err := p.client.getGroup(ctx, groupID)
				if err != nil {
					continue
				}
				seenGroups[groupID] = struct{}{}
				results = append(results, p.groupSearchResult(ctx, group))
			}
			if len(results) > 0 {
				fmt.Printf("[SHOKO][SEARCH] TMDB show %d -> %d Shoko group match(es)\n", tmdbID, len(results))
				return results, nil
			}
		}
		fmt.Printf("[SHOKO][SEARCH] No direct Shoko mapping for TMDB show %d; falling back to title search\n", tmdbID)
	}

	if anidbRaw := providerIDs["anidb"]; anidbRaw != "" {
		if anidbID, err := strconv.Atoi(anidbRaw); err == nil {
			series, err := p.client.getSeriesByAniDB(ctx, anidbID)
			if err != nil {
				return nil, err
			}
			groupID := series.IDs.TopLevelGroup
			if groupID == 0 {
				groupID = series.IDs.ParentGroup
			}
			if groupID != 0 {
				group, err := p.client.getGroup(ctx, groupID)
				if err == nil {
					return []SearchResult{p.groupSearchResult(ctx, group)}, nil
				}
			}
		}
	}

	candidates := titleSearchCandidates(query)
	if len(candidates) == 0 {
		return nil, nil
	}

	bestSeriesHits := make(map[int]rankedSeriesHit)
	var firstSearchErr error
	for candidateIndex, candidate := range candidates {
		candidateHits, searchErr := p.client.searchSeries(ctx, candidate, 25)
		if searchErr != nil {
			if firstSearchErr == nil {
				firstSearchErr = searchErr
			}
			continue
		}
		for _, hit := range candidateHits {
			if hit.IDs.ID == 0 {
				continue
			}
			title := hit.Match
			if title == "" {
				title = hit.Name
			}
			ranked := rankedSeriesHit{
				hit:            hit,
				candidateIndex: candidateIndex,
				titleScore:     fuzzyMatchScore(title, candidate),
			}
			previous, exists := bestSeriesHits[hit.IDs.ID]
			if !exists || betterSeriesHit(ranked.hit, ranked.candidateIndex, ranked.titleScore,
				previous.hit, previous.candidateIndex, previous.titleScore) {
				bestSeriesHits[hit.IDs.ID] = ranked
			}
		}
	}
	if len(bestSeriesHits) == 0 && firstSearchErr != nil {
		return nil, fmt.Errorf("Search searchSeries: %w", firstSearchErr)
	}
	searchHits := make([]rankedSeriesHit, 0, len(bestSeriesHits))
	for _, hit := range bestSeriesHits {
		searchHits = append(searchHits, hit)
	}
	sort.SliceStable(searchHits, func(i, j int) bool {
		return betterSeriesHit(searchHits[i].hit, searchHits[i].candidateIndex, searchHits[i].titleScore,
			searchHits[j].hit, searchHits[j].candidateIndex, searchHits[j].titleScore)
	})

	// Shoko already ranks title/synonym matches. Convert each Series hit to its
	// top-level Group and deduplicate sequel/season hits that belong to the same show.
	type groupHit struct {
		groupID    int
		exact      bool
		distance   float64
		titleScore float64
		candidate  int
		seriesYear int
	}
	hits := make([]groupHit, 0, len(searchHits))
	seenGroups := make(map[int]struct{}, len(searchHits))
	for _, ranked := range searchHits {
		hit := ranked.hit
		groupID := hit.IDs.TopLevelGroup
		if groupID == 0 {
			groupID = hit.IDs.ParentGroup
		}
		if groupID == 0 {
			continue
		}
		if _, seen := seenGroups[groupID]; seen {
			continue
		}
		seenGroups[groupID] = struct{}{}

		seriesYear := 0
		if hit.IDs.ID != 0 {
			if series, getErr := p.client.getSeries(ctx, hit.IDs.ID); getErr == nil {
				seriesYear = extractYear(series.AirDate)
				if seriesYear == 0 && series.AniDB != nil {
					seriesYear = extractYear(series.AniDB.AirDate)
				}
			}
		}

		// An explicit year is a useful disambiguator, but exact Shoko title matches
		// remain eligible because Group metadata may represent a multi-season show.
		if year > 0 && seriesYear > 0 && seriesYear != int(year) && !hit.ExactMatch {
			continue
		}

		hits = append(hits, groupHit{
			groupID:    groupID,
			exact:      hit.ExactMatch,
			distance:   hit.Distance,
			titleScore: ranked.titleScore,
			candidate:  ranked.candidateIndex,
			seriesYear: seriesYear,
		})
	}

	// Preserve Shoko's ranking, with exact matches pinned ahead of fuzzy matches.
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].exact != hits[j].exact {
			return hits[i].exact
		}
		if hits[i].titleScore != hits[j].titleScore {
			return hits[i].titleScore > hits[j].titleScore
		}
		if hits[i].candidate != hits[j].candidate {
			return hits[i].candidate < hits[j].candidate
		}
		return hits[i].distance < hits[j].distance
	})
	if len(hits) > 10 {
		hits = hits[:10]
	}

	results := make([]SearchResult, 0, len(hits))
	for _, hit := range hits {
		group, getErr := p.client.getGroup(ctx, hit.groupID)
		if getErr != nil {
			fmt.Printf("[SHOKO][SEARCH] Skipping group %d: %v\n", hit.groupID, getErr)
			continue
		}
		result := p.groupSearchResult(ctx, group)
		ranked := searchHitsForGroup(searchHits, hit.groupID)
		if ranked != nil {
			result.TitleAliases = appendSearchAlias(result.TitleAliases, ranked.hit.Match, "", "alternate")
			result.TitleAliases = appendSearchAlias(result.TitleAliases, ranked.hit.Name, "", "alternate")
		}
		if year > 0 && result.Year > 0 && result.Year != int(year) && !hit.exact {
			continue
		}
		results = append(results, result)
	}

	fmt.Printf("[SHOKO][SEARCH] Candidates=%q ranked %d series hits -> %d unique group results\n", candidates, len(searchHits), len(results))
	return results, nil
}

func searchHitsForGroup(hits []rankedSeriesHit, groupID int) *rankedSeriesHit {
	for i := range hits {
		candidateGroupID := hits[i].hit.IDs.TopLevelGroup
		if candidateGroupID == 0 {
			candidateGroupID = hits[i].hit.IDs.ParentGroup
		}
		if candidateGroupID == groupID {
			return &hits[i]
		}
	}
	return nil
}

func appendSearchAlias(aliases []SearchTitleAlias, title, language, kind string) []SearchTitleAlias {
	title = strings.TrimSpace(title)
	if title == "" {
		return aliases
	}
	normalized := normalizeTitle(title)
	for _, alias := range aliases {
		if normalizeTitle(alias.Title) == normalized {
			return aliases
		}
	}
	return append(aliases, SearchTitleAlias{Title: title, Language: language, Kind: kind})
}

func betterSeriesHit(a shokoSeriesSearchResult, aCandidate int, aTitleScore float64,
	b shokoSeriesSearchResult, bCandidate int, bTitleScore float64) bool {
	if a.ExactMatch != b.ExactMatch {
		return a.ExactMatch
	}
	if aTitleScore != bTitleScore {
		return aTitleScore > bTitleScore
	}
	if aCandidate != bCandidate {
		return aCandidate < bCandidate
	}
	return a.Distance < b.Distance
}

func (p *Provider) groupSearchResult(ctx context.Context, group *shokoGroup) SearchResult {
	if group != nil && group.IDs.ID != 0 {
		if full, err := p.client.getGroup(ctx, group.IDs.ID); err == nil {
			group = full
		}
	}
	name := group.Name
	if name == "" {
		name = group.SortName
	}
	result := SearchResult{
		Name:          name,
		OriginalTitle: group.SortName,
		Overview:      sanitizeDescription(group.Description),
		ProviderIDs: map[string]string{
			"shoko":       strconv.Itoa(group.IDs.ID),
			"shoko_group": strconv.Itoa(group.IDs.ID),
		},
		ItemType: "series",
	}
	if img := firstImage(group.Images.Posters); img != nil {
		result.ImageURL = p.buildImageURL(*img, "Poster")
	}
	if group.IDs.MainAnime != 0 {
		result.ProviderIDs["anidb"] = strconv.Itoa(group.IDs.MainAnime)
	}
	if group.IDs.MainSeries != 0 {
		if mainSeries, err := p.client.getSeries(ctx, group.IDs.MainSeries); err == nil {
			result.Year = extractYear(mainSeries.AirDate)
			result.TitleAliases = appendSearchAlias(result.TitleAliases, mainSeries.Name, "", "alternate")
			if mainSeries.AniDB != nil {
				for _, title := range mainSeries.AniDB.Titles {
					kind := "alternate"
					if title.Default || title.Preferred {
						kind = "localized"
					}
					result.TitleAliases = appendSearchAlias(result.TitleAliases, title.Name, title.Language, kind)
				}
			}
			if len(mainSeries.IDs.TMDB.Show) > 0 {
				result.ProviderIDs["tmdb"] = strconv.Itoa(mainSeries.IDs.TMDB.Show[0])
			}
		}
	}
	return result
}

func (p *Provider) searchMovieSeries(ctx context.Context, query string, providerIDs map[string]string) ([]SearchResult, error) {
	if anidbRaw := providerIDs["anidb"]; anidbRaw != "" {
		if anidbID, err := strconv.Atoi(anidbRaw); err == nil {
			series, err := p.client.getSeriesByAniDB(ctx, anidbID)
			if err != nil {
				return nil, err
			}
			if isMovieType(series) {
				return []SearchResult{p.seriesSearchResult(series)}, nil
			}
		}
	}
	queries := titleSearchCandidates(query)
	if len(queries) == 0 {
		return nil, nil
	}

	var hits []shokoSeriesSearchResult
	seenHit := make(map[int]struct{})
	var firstErr error
	for _, candidate := range queries {
		candidateHits, err := p.client.searchSeries(ctx, candidate, 25)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, hit := range candidateHits {
			if _, exists := seenHit[hit.IDs.ID]; exists {
				continue
			}
			seenHit[hit.IDs.ID] = struct{}{}
			hits = append(hits, hit)
		}
	}
	if len(hits) == 0 && firstErr != nil {
		return nil, fmt.Errorf("searchMovieSeries searchSeries: %w", firstErr)
	}

	type movieHit struct {
		series   shokoSeries
		exact    bool
		distance float64
	}
	candidates := make([]movieHit, 0, len(hits))
	seen := make(map[int]struct{}, len(hits))
	for _, hit := range hits {
		if hit.IDs.ID == 0 {
			continue
		}
		if _, ok := seen[hit.IDs.ID]; ok {
			continue
		}
		seen[hit.IDs.ID] = struct{}{}

		series, getErr := p.client.getSeries(ctx, hit.IDs.ID)
		if getErr != nil || !isMovieType(series) {
			continue
		}
		candidates = append(candidates, movieHit{series: *series, exact: hit.ExactMatch, distance: hit.Distance})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].exact != candidates[j].exact {
			return candidates[i].exact
		}
		return candidates[i].distance < candidates[j].distance
	})
	if len(candidates) > 10 {
		candidates = candidates[:10]
	}

	results := make([]SearchResult, 0, len(candidates))
	for _, candidate := range candidates {
		results = append(results, p.seriesSearchResult(&candidate.series))
	}
	return results, nil
}

func (p *Provider) seriesSearchResult(series *shokoSeries) SearchResult {
	ids := map[string]string{
		"shoko":        strconv.Itoa(series.IDs.ID),
		"shoko_series": strconv.Itoa(series.IDs.ID),
		"anidb":        strconv.Itoa(series.IDs.AniDB),
	}
	if len(series.IDs.TMDB.Movie) > 0 {
		ids["tmdb_movie"] = strconv.Itoa(series.IDs.TMDB.Movie[0])
	}
	poster := ""
	if img := firstImage(series.Images.Posters); img != nil {
		poster = p.buildImageURL(*img, "Poster")
	}
	return SearchResult{
		Name:          series.Name,
		OriginalTitle: series.Name,
		Year:          extractYear(series.AirDate),
		Overview:      sanitizeDescription(series.Description),
		ProviderIDs:   ids,
		ImageURL:      poster,
		ItemType:      "movie",
	}
}

// ============================================================================
// GETMETADATA
// ============================================================================

func (p *Provider) GetMetadata(ctx context.Context, providerID, itemType string, providerIDs map[string]string, language string) (*MetadataResult, error) {
	fmt.Printf("[SHOKO][META] providerID=%q itemType=%q providerIDs=%+v\n", providerID, itemType, providerIDs)
	if strings.EqualFold(itemType, "movie") || providerIDs["shoko_series"] != "" {
		return p.getSeriesMetadata(ctx, providerID, providerIDs)
	}

	// Prefer an explicit Shoko Group ID. Otherwise, if Silo already knows the
	// TMDB show ID, use that as the cross-provider identity bridge before
	// considering the legacy generic "shoko" ID (which older plugin versions
	// used for a Shoko Series rather than a Group).
	groupID, ok := providerInt("", providerIDs, "shoko_group")
	if !ok {
		if tmdbID, hasTMDB := providerTMDBShowID(providerIDs); hasTMDB {
			seriesMatches, matchErr := p.client.getSeriesByTMDBShow(ctx, tmdbID)
			if matchErr == nil && len(seriesMatches) > 0 {
				groupID = seriesMatches[0].IDs.TopLevelGroup
				if groupID == 0 {
					groupID = seriesMatches[0].IDs.ParentGroup
				}
				ok = groupID != 0
				if ok {
					fmt.Printf("[SHOKO][META] Resolved existing TMDB show %d -> Shoko group %d\n", tmdbID, groupID)
				}
			}
		}
	}
	if !ok {
		groupID, ok = providerInt(providerID, providerIDs, "shoko")
	}
	if !ok {
		return nil, fmt.Errorf("missing Shoko group provider ID and no linked TMDB show match")
	}
	group, err := p.client.getGroup(ctx, groupID)
	if err != nil {
		// Backward compatibility for metadata already matched by older plugin versions.
		if providerIDs["shoko_group"] == "" {
			return p.getSeriesMetadata(ctx, providerID, providerIDs)
		}
		return nil, err
	}
	seasons, err := p.seasonSeriesForGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}

	ids := map[string]string{
		"shoko":       strconv.Itoa(groupID),
		"shoko_group": strconv.Itoa(groupID),
	}
	if group.IDs.MainAnime != 0 {
		ids["anidb"] = strconv.Itoa(group.IDs.MainAnime)
	}

	var mainSeries *shokoSeries
	if group.IDs.MainSeries != 0 {
		mainSeries, _ = p.client.getSeries(ctx, group.IDs.MainSeries)
	}
	if mainSeries == nil && len(seasons) > 0 {
		mainSeries = seasons[0]
	}
	if mainSeries != nil && len(mainSeries.IDs.TMDB.Show) > 0 {
		ids["tmdb"] = strconv.Itoa(mainSeries.IDs.TMDB.Show[0])
	}

	firstAir, lastAir := "", ""
	for _, season := range seasons {
		firstAir = earliestDate(firstAir, season.AirDate)
		lastAir = latestDate(lastAir, season.EndDate)
	}
	if mainSeries != nil {
		firstAir = earliestDate(firstAir, mainSeries.AirDate)
		lastAir = latestDate(lastAir, mainSeries.EndDate)
	}

	runtime := 0
	genreSet := map[string]struct{}{}
	genres := []string{}
	addGenres := func(series *shokoSeries) {
		if series == nil {
			return
		}
		for _, genre := range buildGenresFromTags(series.Tags) {
			if _, exists := genreSet[genre]; !exists {
				genreSet[genre] = struct{}{}
				genres = append(genres, genre)
			}
		}
	}
	addGenres(mainSeries)
	for _, season := range seasons {
		addGenres(season)
	}
	sort.Strings(genres)
	if mainSeries != nil {
		if episodes, err := p.getSeriesEpisodes(ctx, mainSeries.IDs.ID); err == nil {
			total, count := 0, 0
			for _, ep := range episodes {
				if ep.AniDB == nil || !strings.EqualFold(ep.AniDB.Type, "Episode") {
					continue
				}
				if mins := parseDuration(ep.Duration); mins > 0 {
					total += mins
					count++
				}
			}
			if count > 0 {
				runtime = (total + count/2) / count
			}
		}
	}

	poster, backdrop := "", ""
	if img := firstImage(group.Images.Posters); img != nil {
		poster = p.buildImageURL(*img, "Poster")
	}
	if img := firstImage(group.Images.Backdrops); img != nil {
		backdrop = p.buildImageURL(*img, "Backdrop")
	}
	name := group.Name
	if name == "" {
		name = group.SortName
	}
	status := "Ended"
	if lastAir == "" {
		status = "Ongoing"
	}
	return &MetadataResult{
		Title:         name,
		OriginalTitle: group.SortName,
		Year:          extractYear(firstAir),
		Overview:      sanitizeDescription(group.Description),
		Runtime:       runtime,
		Genres:        genres,
		Studios:       []string{},
		ProviderIDs:   ids,
		PosterPath:    poster,
		BackdropPath:  backdrop,
		SeasonCount:   len(seasons),
		FirstAirDate:  firstAir,
		LastAirDate:   lastAir,
		ReleaseDate:   firstAir,
		ShowStatus:    status,
		ItemType:      "series",
	}, nil
}

func (p *Provider) getSeriesMetadata(ctx context.Context, providerID string, providerIDs map[string]string) (*MetadataResult, error) {
	var series *shokoSeries
	var err error
	if anidbRaw := providerIDs["anidb"]; anidbRaw != "" {
		if anidbID, parseErr := strconv.Atoi(anidbRaw); parseErr == nil {
			series, err = p.client.getSeriesByAniDB(ctx, anidbID)
		}
	}
	if series == nil && err == nil {
		if seriesID, ok := providerInt(providerID, providerIDs, "shoko_series", "shoko"); ok {
			series, err = p.client.getSeries(ctx, seriesID)
		}
	}
	if err != nil {
		return nil, err
	}
	if series == nil {
		return nil, fmt.Errorf("series resolution returned nil")
	}

	movieType := isMovieType(series)
	ids := map[string]string{
		"shoko":        strconv.Itoa(series.IDs.ID),
		"shoko_series": strconv.Itoa(series.IDs.ID),
		"anidb":        strconv.Itoa(series.IDs.AniDB),
	}
	if len(series.IDs.TMDB.Show) > 0 {
		ids["tmdb"] = strconv.Itoa(series.IDs.TMDB.Show[0])
	}
	if len(series.IDs.TMDB.Movie) > 0 {
		ids["tmdb_movie"] = strconv.Itoa(series.IDs.TMDB.Movie[0])
	}
	poster, backdrop := "", ""
	if img := firstImage(series.Images.Posters); img != nil {
		poster = p.buildImageURL(*img, "Poster")
	}
	if img := firstImage(series.Images.Backdrops); img != nil {
		backdrop = p.buildImageURL(*img, "Backdrop")
	}

	runtime := 0
	if episodes, epErr := p.getSeriesEpisodes(ctx, series.IDs.ID); epErr == nil {
		for _, ep := range episodes {
			if ep.AniDB != nil && strings.EqualFold(ep.AniDB.Type, "Episode") {
				runtime = parseDuration(ep.Duration)
				if runtime > 0 {
					break
				}
			}
		}
	}
	status := "Ended"
	if series.EndDate == "" {
		status = "Ongoing"
	}
	result := &MetadataResult{
		Title:         series.Name,
		OriginalTitle: series.Name,
		Year:          extractYear(series.AirDate),
		Overview:      sanitizeDescription(series.Description),
		Runtime:       runtime,
		Genres:        buildGenresFromTags(series.Tags),
		Studios:       []string{},
		ProviderIDs:   ids,
		PosterPath:    poster,
		BackdropPath:  backdrop,
		ReleaseDate:   series.AirDate,
		ShowStatus:    status,
	}
	if movieType {
		result.ItemType = "movie"
	} else {
		result.ItemType = "series"
		result.SeasonCount = 1
		result.FirstAirDate = series.AirDate
		result.LastAirDate = series.EndDate
	}
	return result, nil
}

func (p *Provider) getSeriesEpisodes(ctx context.Context, shokoID int) ([]shokoFullEpisode, error) {
	endpoint := fmt.Sprintf("/api/v3/Series/%d/Episode?includeMissing=true&includeDataFrom=AniDB", shokoID)
	resp, err := p.client.doGet(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("getSeriesEpisodes HTTP error: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("getSeriesEpisodes returned %d: %s", resp.StatusCode, string(body))
	}
	var episodeRoot shokoEpisodeRoot
	if err := json.NewDecoder(resp.Body).Decode(&episodeRoot); err != nil {
		return nil, fmt.Errorf("getSeriesEpisodes JSON unmarshal: %w", err)
	}
	return episodeRoot.List, nil
}

// ============================================================================
// SEASONS / EPISODES / IMAGES
// ============================================================================

func (p *Provider) GetSeasons(ctx context.Context, seriesProviderID string, providerIDs map[string]string, language string) ([]SeasonResult, error) {
	groupID, ok := providerInt(seriesProviderID, providerIDs, "shoko_group", "shoko")
	if !ok {
		return nil, fmt.Errorf("invalid Shoko group provider ID %q", seriesProviderID)
	}
	seasons, err := p.seasonSeriesForGroup(ctx, groupID)
	if err != nil {
		// Compatibility with an old direct-series match.
		if providerIDs["shoko_group"] == "" {
			if series, seriesErr := p.client.getSeries(ctx, groupID); seriesErr == nil && !isMovieType(series) {
				poster := ""
				if img := firstImage(series.Images.Posters); img != nil {
					poster = p.buildImageURL(*img, "Poster")
				}
				return []SeasonResult{{
					ContentID:    strconv.Itoa(series.IDs.ID),
					SeasonNumber: 1,
					Title:        series.Name,
					Overview:     sanitizeDescription(series.Description),
					AirDate:      series.AirDate,
					PosterPath:   poster,
					ProviderIDs: map[string]string{
						"shoko":        strconv.Itoa(series.IDs.ID),
						"shoko_series": strconv.Itoa(series.IDs.ID),
					},
				}}, nil
			}
		}
		return nil, err
	}

	results := make([]SeasonResult, 0, len(seasons))
	for idx, series := range seasons {
		poster := ""
		if img := firstImage(series.Images.Posters); img != nil {
			poster = p.buildImageURL(*img, "Poster")
		}
		ids := map[string]string{
			"shoko":        strconv.Itoa(series.IDs.ID),
			"shoko_series": strconv.Itoa(series.IDs.ID),
			"shoko_group":  strconv.Itoa(groupID),
		}
		if series.IDs.AniDB != 0 {
			ids["anidb"] = strconv.Itoa(series.IDs.AniDB)
		}
		results = append(results, SeasonResult{
			ContentID:    strconv.Itoa(series.IDs.ID),
			SeasonNumber: idx + 1,
			Title:        series.Name,
			Overview:     sanitizeDescription(series.Description),
			AirDate:      series.AirDate,
			PosterPath:   poster,
			ProviderIDs:  ids,
		})
	}
	fmt.Printf("[SHOKO][SEASONS] Group %d -> %d Silo seasons\n", groupID, len(results))
	return results, nil
}

func (p *Provider) GetEpisodes(ctx context.Context, seriesProviderID string, seasonNumber int32, language string) ([]EpisodeResult, error) {
	if seasonNumber < 1 {
		return []EpisodeResult{}, nil
	}
	groupID, err := strconv.Atoi(seriesProviderID)
	if err != nil {
		return nil, fmt.Errorf("invalid series provider ID: %w", err)
	}

	seasonSeries, seasonErr := p.seasonSeriesForGroup(ctx, groupID)
	var shokoSeriesID int
	if seasonErr == nil && int(seasonNumber) <= len(seasonSeries) {
		shokoSeriesID = seasonSeries[seasonNumber-1].IDs.ID
	} else if seasonErr != nil {
		// Backward compatibility: older matches used a Shoko Series ID directly.
		if series, directErr := p.client.getSeries(ctx, groupID); directErr == nil && seasonNumber == 1 && !isMovieType(series) {
			shokoSeriesID = series.IDs.ID
		} else {
			return nil, seasonErr
		}
	} else {
		return []EpisodeResult{}, nil
	}

	episodes, err := p.getSeriesEpisodes(ctx, shokoSeriesID)
	if err != nil {
		return nil, err
	}
	normal := make([]shokoFullEpisode, 0, len(episodes))
	for _, ep := range episodes {
		if ep.AniDB == nil || !strings.EqualFold(ep.AniDB.Type, "Episode") || ep.AniDB.EpisodeNumber <= 0 {
			continue
		}
		normal = append(normal, ep)
	}
	sort.SliceStable(normal, func(i, j int) bool {
		if normal[i].AniDB.EpisodeNumber != normal[j].AniDB.EpisodeNumber {
			return normal[i].AniDB.EpisodeNumber < normal[j].AniDB.EpisodeNumber
		}
		return normal[i].IDs.ID < normal[j].IDs.ID
	})

	results := make([]EpisodeResult, 0, len(normal))
	for _, ep := range normal {
		still := ""
		if img := firstImage(ep.Images.Thumbnails); img != nil {
			still = p.buildImageURL(*img, "Thumbnail")
		}
		title := ep.Name
		overview := ep.Description
		if ep.AniDB != nil {
			if ep.AniDB.Title != "" {
				title = ep.AniDB.Title
			}
			if ep.AniDB.Description != "" {
				overview = ep.AniDB.Description
			}
		}
		ids := map[string]string{
			"shoko":         strconv.Itoa(ep.IDs.ID),
			"shoko_episode": strconv.Itoa(ep.IDs.ID),
			"shoko_series":  strconv.Itoa(shokoSeriesID),
			"shoko_group":   strconv.Itoa(groupID),
		}
		if ep.IDs.AniDB != 0 {
			ids["anidb_episode"] = strconv.Itoa(ep.IDs.AniDB)
		}
		if len(ep.IDs.TMDB.Episode) > 0 {
			ids["tmdb_episode"] = strconv.Itoa(ep.IDs.TMDB.Episode[0])
		}
		results = append(results, EpisodeResult{
			ContentID:     strconv.Itoa(ep.IDs.ID),
			SeasonNumber:  int(seasonNumber),
			EpisodeNumber: ep.AniDB.EpisodeNumber,
			Title:         title,
			Overview:      sanitizeDescription(overview),
			AirDate:       ep.AniDB.AirDate,
			Runtime:       parseDuration(ep.Duration),
			StillPath:     still,
			ProviderIDs:   ids,
		})
	}
	fmt.Printf("[SHOKO][EPISODES] Group %d season %d -> Shoko series %d -> %d normal episodes\n",
		groupID, seasonNumber, shokoSeriesID, len(results))
	return results, nil
}

func (p *Provider) GetImages(ctx context.Context, providerID, itemType string, providerIDs map[string]string, language string) ([]ImageResult, error) {
	var imgResp shokoImagesResponse
	if strings.EqualFold(itemType, "movie") || providerIDs["shoko_series"] != "" {
		seriesID, ok := providerInt(providerID, providerIDs, "shoko_series", "shoko")
		if !ok {
			return nil, fmt.Errorf("invalid Shoko series provider ID")
		}
		endpoint := fmt.Sprintf("/api/v3/Series/%d/Images", seriesID)
		resp, err := p.client.doGet(ctx, endpoint)
		if err != nil {
			return nil, fmt.Errorf("GetImages HTTP error: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("GetImages returned %d: %s", resp.StatusCode, string(body))
		}
		if err := json.NewDecoder(resp.Body).Decode(&imgResp); err != nil {
			return nil, fmt.Errorf("GetImages JSON unmarshal: %w", err)
		}
	} else {
		groupID, ok := providerInt(providerID, providerIDs, "shoko_group", "shoko")
		if !ok {
			return nil, fmt.Errorf("invalid Shoko group provider ID")
		}
		group, err := p.client.getGroup(ctx, groupID)
		if err != nil {
			return nil, err
		}
		imgResp.Posters = group.Images.Posters
		imgResp.Backdrops = group.Images.Backdrops
		imgResp.Banners = group.Images.Banners
		imgResp.Logos = group.Images.Logos
	}

	var images []ImageResult
	appendImages := func(kind, fallback string, list []shokoImage) {
		for _, img := range list {
			if img.Disabled {
				continue
			}
			if imgURL := p.buildImageURL(img, fallback); imgURL != "" {
				images = append(images, ImageResult{
					Kind: kind, URL: imgURL, Language: imageLanguage(img), Width: img.Width, Height: img.Height,
				})
			}
		}
	}
	appendImages("poster", "Poster", imgResp.Posters)
	appendImages("backdrop", "Backdrop", imgResp.Backdrops)
	appendImages("banner", "Banner", imgResp.Banners)
	appendImages("logo", "Logo", imgResp.Logos)
	return images, nil
}
