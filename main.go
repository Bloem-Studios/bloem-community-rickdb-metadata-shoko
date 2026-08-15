package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/RickDB/silo-plugin-metadata-shoko/provider"
	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	publicmanifest "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtime"
)

var version = "0.2.6"

//go:embed manifest.json
var manifestJSON []byte

// ============================================================================
// GLOBAL CONFIG
// ============================================================================

type globalConfig struct {
	ShokoServerURL     string
	ShokoAPIKey        string
	ShowVerifiedTags   bool
	HideSpoilerTags    bool
	TagWeightThreshold int
}

func defaultGlobalConfig() *globalConfig {
	return &globalConfig{
		ShowVerifiedTags:   true,
		HideSpoilerTags:    true,
		TagWeightThreshold: 400,
	}
}

func configFromEntries(entries []*pluginv1.ConfigEntry) *globalConfig {
	cfg := defaultGlobalConfig()

	for _, entry := range entries {
		if entry == nil || entry.GetValue() == nil {
			continue
		}
		m := entry.GetValue().AsMap()

		switch entry.GetKey() {
		case "server":
			if v, ok := m["server_url"]; ok && v != nil {
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					cfg.ShokoServerURL = strings.TrimSuffix(strings.TrimSpace(s), "/")
				}
			}
			if v, ok := m["api_key"]; ok && v != nil {
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					cfg.ShokoAPIKey = strings.TrimSpace(s)
				}
			}
		case "tags":
			cfg.ShowVerifiedTags = parseAnyBool(m["show_verified_tags"], true)
			cfg.HideSpoilerTags = parseAnyBool(m["hide_spoiler_tags"], true)
			cfg.TagWeightThreshold = parseAnyInt(m["tag_weight_threshold"], 400)
		}
	}

	return cfg
}

func parseAnyBool(v interface{}, defaultVal bool) bool {
	if v == nil {
		return defaultVal
	}
	switch val := v.(type) {
	case bool:
		return val
	case string:
		return strToBool(val, defaultVal)
	case float64:
		return val != 0
	default:
		return defaultVal
	}
}

func parseAnyInt(v interface{}, defaultVal int) int {
	if v == nil {
		return defaultVal
	}
	switch val := v.(type) {
	case int:
		return val
	case int32:
		return int(val)
	case int64:
		return int(val)
	case float64:
		return int(val)
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(val))
		if err != nil {
			return defaultVal
		}
		return n
	default:
		return defaultVal
	}
}

func strToBool(s string, defaultVal bool) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	default:
		return defaultVal
	}
}

func configToTagFilter(c *globalConfig) provider.TagFilterConfig {
	return provider.TagFilterConfig{
		ShowVerifiedTags:   c.ShowVerifiedTags,
		HideSpoilerTags:    c.HideSpoilerTags,
		TagWeightThreshold: c.TagWeightThreshold,
	}
}

// ============================================================================
// SERVERS
// ============================================================================

type runtimeServer struct {
	pluginv1.UnimplementedRuntimeServer
	manifest  *pluginv1.PluginManifest
	globalCfg *globalConfig
	provider  *provider.Provider
	mu        sync.RWMutex
}

type metadataServer struct {
	pluginv1.UnimplementedMetadataProviderServer
	pluginv1.UnimplementedImageResolverServer
	runtime *runtimeServer
}

func (s *runtimeServer) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: s.manifest}, nil
}

func (s *runtimeServer) Configure(_ context.Context, req *pluginv1.ConfigureRequest) (*pluginv1.ConfigureResponse, error) {
	cfg := configFromEntries(req.GetConfig())

	s.mu.Lock()
	s.globalCfg = cfg
	s.provider = nil
	s.mu.Unlock()

	fmt.Printf("[CONFIG] Global config updated: URL=%s, ShowVerified=%v, HideSpoilers=%v, WeightThresh=%d\n",
		cfg.ShokoServerURL, cfg.ShowVerifiedTags, cfg.HideSpoilerTags, cfg.TagWeightThreshold)

	return &pluginv1.ConfigureResponse{}, nil
}

func (s *runtimeServer) ensureProvider() (*provider.Provider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.globalCfg == nil || s.globalCfg.ShokoServerURL == "" {
		return nil, fmt.Errorf("global configuration not set — please configure Shoko Server address and API key in plugin settings")
	}

	if s.provider == nil {
		s.provider = provider.NewProvider(
			s.globalCfg.ShokoServerURL,
			s.globalCfg.ShokoAPIKey,
			configToTagFilter(s.globalCfg),
		)
	}

	return s.provider, nil
}

func (s *runtimeServer) getGlobalCfg() *globalConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.globalCfg
}

// ============================================================================
// METADATA PROVIDER RPCs
// ============================================================================

func (s *metadataServer) Search(ctx context.Context, req *pluginv1.SearchMetadataRequest) (*pluginv1.SearchMetadataResponse, error) {
	p, err := s.runtime.ensureProvider()
	if err != nil {
		return nil, err
	}

	results, err := p.Search(ctx, req.GetQuery(), req.GetItemType(), req.GetYear(),
		stringMapFromStruct(req.GetProviderIds()), req.GetLanguage())
	if err != nil {
		return nil, err
	}

	baseURL := s.runtime.getGlobalCfg().ShokoServerURL

	response := &pluginv1.SearchMetadataResponse{
		Results: make([]*pluginv1.ProviderSearchResult, 0, len(results)),
	}
	for _, result := range results {
		providerIDs, err := stringStruct(result.ProviderIDs)
		if err != nil {
			return nil, err
		}
		titleAliases := make([]*pluginv1.TitleAlias, 0, len(result.TitleAliases))
		for _, alias := range result.TitleAliases {
			titleAliases = append(titleAliases, &pluginv1.TitleAlias{
				Title:    alias.Title,
				Language: alias.Language,
				Kind:     alias.Kind,
			})
		}
		response.Results = append(response.Results, &pluginv1.ProviderSearchResult{
			ProviderId:    result.ProviderIDs["shoko"],
			ItemType:      itemTypeForResult(result.ItemType, req.GetItemType()),
			Title:         result.Name,
			Year:          int32(result.Year),
			Overview:      result.Overview,
			ProviderIds:   providerIDs,
			ImageUrl:      shokoCanonicalPath(baseURL, result.ImageURL),
			OriginalTitle: result.OriginalTitle,
			TitleAliases:  titleAliases,
		})
	}
	return response, nil
}

func (s *metadataServer) GetMetadata(ctx context.Context, req *pluginv1.GetMetadataRequest) (*pluginv1.GetMetadataResponse, error) {
	p, err := s.runtime.ensureProvider()
	if err != nil {
		return nil, err
	}

	result, err := p.GetMetadata(ctx, req.GetProviderId(), req.GetItemType(),
		stringMapFromStruct(req.GetProviderIds()), req.GetLanguage())
	if err != nil || result == nil {
		return nil, err
	}

	providerIDs, err := stringStruct(result.ProviderIDs)
	if err != nil {
		return nil, err
	}

	itemType := req.GetItemType()
	if result.ItemType != "" {
		itemType = result.ItemType
	}

	baseURL := s.runtime.getGlobalCfg().ShokoServerURL
	genres := mergeGenres(existingGenresFromRequest(req), result.Genres)

	item := &pluginv1.MetadataItem{
		ProviderId:    result.ProviderIDs["shoko"],
		ItemType:      itemType,
		Title:         result.Title,
		OriginalTitle: result.OriginalTitle,
		Year:          int32(result.Year),
		Overview:      result.Overview,
		Runtime:       int32(result.Runtime),
		Genres:        genres,
		Studios:       append([]string(nil), result.Studios...),
		ProviderIds:   providerIDs,
		PosterPath:    shokoCanonicalPath(baseURL, result.PosterPath),
		BackdropPath:  shokoCanonicalPath(baseURL, result.BackdropPath),
		SeasonCount:   int32(result.SeasonCount),
		FirstAirDate:  result.FirstAirDate,
		LastAirDate:   result.LastAirDate,
		ReleaseDate:   result.ReleaseDate,
		Status:        result.ShowStatus,
	}

	if len(item.Genres) > 0 {
		fmt.Printf("[SHOKO][META] Returning %d genres for %q (shoko=%s): %v\n",
			len(item.Genres), item.Title, result.ProviderIDs["shoko"], item.Genres)
	}

	return &pluginv1.GetMetadataResponse{Item: item}, nil
}

// existingGenresField is the forward-compatible protobuf field reserved for:
//
//	repeated string existing_genres = 6;
//
// Until the SDK exposes a generated accessor, newer hosts can send the field
// and older generated clients retain it in the message's unknown field set.
const existingGenresField protowire.Number = 6

func existingGenresFromRequest(req *pluginv1.GetMetadataRequest) []string {
	if req == nil {
		return nil
	}

	raw := req.ProtoReflect().GetUnknown()
	var genres []string
	for len(raw) > 0 {
		number, wireType, n := protowire.ConsumeTag(raw)
		if n < 0 {
			break
		}
		raw = raw[n:]

		if number == existingGenresField && wireType == protowire.BytesType {
			value, consumed := protowire.ConsumeBytes(raw)
			if consumed < 0 {
				break
			}
			genres = append(genres, string(value))
			raw = raw[consumed:]
			continue
		}

		consumed := protowire.ConsumeFieldValue(number, wireType, raw)
		if consumed < 0 {
			break
		}
		raw = raw[consumed:]
	}
	return genres
}

func mergeGenres(existing, shoko []string) []string {
	merged := make([]string, 0, len(existing)+len(shoko))
	seen := make(map[string]struct{}, len(existing)+len(shoko))
	for _, genres := range [][]string{existing, shoko} {
		for _, genre := range genres {
			genre = strings.TrimSpace(genre)
			key := strings.ToLower(genre)
			if key == "" {
				continue
			}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			merged = append(merged, genre)
		}
	}
	sort.Slice(merged, func(i, j int) bool {
		left, right := strings.ToLower(merged[i]), strings.ToLower(merged[j])
		if left == right {
			return merged[i] < merged[j]
		}
		return left < right
	})
	return merged
}

func (s *metadataServer) GetSeasons(ctx context.Context, req *pluginv1.GetSeasonsRequest) (*pluginv1.GetSeasonsResponse, error) {
	p, err := s.runtime.ensureProvider()
	if err != nil {
		return nil, err
	}

	results, err := p.GetSeasons(ctx, req.GetSeriesProviderId(),
		stringMapFromStruct(req.GetProviderIds()), req.GetLanguage())
	if err != nil {
		return nil, err
	}

	baseURL := s.runtime.getGlobalCfg().ShokoServerURL

	response := &pluginv1.GetSeasonsResponse{
		Seasons: make([]*pluginv1.SeasonRecord, 0, len(results)),
	}
	for _, result := range results {
		providerIDs, err := stringStruct(result.ProviderIDs)
		if err != nil {
			return nil, err
		}
		response.Seasons = append(response.Seasons, &pluginv1.SeasonRecord{
			ProviderId:   result.ContentID,
			ProviderIds:  providerIDs,
			SeasonNumber: int32(result.SeasonNumber),
			Title:        result.Title,
			Overview:     result.Overview,
			AirDate:      result.AirDate,
			PosterPath:   shokoCanonicalPath(baseURL, result.PosterPath),
		})
	}
	return response, nil
}

func (s *metadataServer) GetEpisodes(ctx context.Context, req *pluginv1.GetEpisodesRequest) (*pluginv1.GetEpisodesResponse, error) {
	p, err := s.runtime.ensureProvider()
	if err != nil {
		return nil, err
	}

	results, err := p.GetEpisodes(ctx, req.GetSeriesProviderId(), req.GetSeasonNumber(), req.GetLanguage())
	if err != nil {
		return nil, err
	}

	baseURL := s.runtime.getGlobalCfg().ShokoServerURL

	response := &pluginv1.GetEpisodesResponse{
		Episodes: make([]*pluginv1.EpisodeRecord, 0, len(results)),
	}
	for _, result := range results {
		providerIDs, err := stringStruct(result.ProviderIDs)
		if err != nil {
			return nil, err
		}
		response.Episodes = append(response.Episodes, &pluginv1.EpisodeRecord{
			ProviderId:    result.ContentID,
			SeasonNumber:  int32(result.SeasonNumber),
			EpisodeNumber: int32(result.EpisodeNumber),
			Title:         result.Title,
			Overview:      result.Overview,
			AirDate:       result.AirDate,
			Runtime:       int32(result.Runtime),
			StillPath:     shokoCanonicalPath(baseURL, result.StillPath),
			ProviderIds:   providerIDs,
		})
	}
	return response, nil
}

func (s *metadataServer) GetImages(ctx context.Context, req *pluginv1.GetImagesRequest) (*pluginv1.GetImagesResponse, error) {
	fmt.Printf("[MAIN][GETIMAGES] Called with providerID=%s itemType=%s\n", req.GetProviderId(), req.GetItemType())

	p, err := s.runtime.ensureProvider()
	if err != nil {
		return nil, err
	}

	images, err := p.GetImages(ctx, req.GetProviderId(), req.GetItemType(),
		stringMapFromStruct(req.GetProviderIds()), req.GetLanguage())
	if err != nil {
		return nil, err
	}

	baseURL := s.runtime.getGlobalCfg().ShokoServerURL
	response := &pluginv1.GetImagesResponse{}
	for _, img := range images {
		response.Images = append(response.Images, &pluginv1.ImageRecord{
			Kind:     img.Kind,
			Url:      shokoCanonicalPath(baseURL, img.URL),
			Language: img.Language,
			Width:    int32(img.Width),
			Height:   int32(img.Height),
		})
	}

	fmt.Printf("[MAIN][GETIMAGES] Returning %d images (all canonicalized to shoko:// paths)\n", len(response.Images))
	return response, nil
}

func (s *metadataServer) GetPersonDetail(_ context.Context, _ *pluginv1.GetPersonDetailRequest) (*pluginv1.GetPersonDetailResponse, error) {
	return &pluginv1.GetPersonDetailResponse{}, nil
}

// ============================================================================
// IMAGE RESOLVER RPCs
// ============================================================================

func (s *metadataServer) ResolveImageURL(_ context.Context, req *pluginv1.ResolveImageURLRequest) (*pluginv1.ResolveImageURLResponse, error) {
	p, err := s.runtime.ensureProvider()
	if err != nil {
		return nil, err
	}
	resolvedURL := p.ResolveImageURL(req.GetPath())
	fmt.Printf("[MAIN][RESOLVE] path=%s variant=%s resolved=%v\n", req.GetPath(), req.GetVariant(), resolvedURL != "")
	return &pluginv1.ResolveImageURLResponse{Url: resolvedURL}, nil
}

func (s *metadataServer) ResolveImageURLs(_ context.Context, req *pluginv1.ResolveImageURLsRequest) (*pluginv1.ResolveImageURLsResponse, error) {
	p, err := s.runtime.ensureProvider()
	if err != nil {
		return nil, err
	}
	urls := make(map[string]string, len(req.GetPaths()))
	for _, path := range req.GetPaths() {
		urls[path] = p.ResolveImageURL(path)
	}
	fmt.Printf("[MAIN][RESOLVE-BATCH] Resolved %d paths\n", len(urls))
	return &pluginv1.ResolveImageURLsResponse{Urls: urls}, nil
}

// ============================================================================
// CANONICAL PATH HELPER
// ============================================================================

func shokoCanonicalPath(baseURL, imageURL string) string {
	if imageURL == "" {
		return ""
	}
	if strings.HasPrefix(imageURL, "shoko://") {
		return imageURL
	}

	path := imageURL
	trimmedBase := strings.TrimSuffix(baseURL, "/")
	path = strings.TrimPrefix(path, trimmedBase)
	path = strings.TrimPrefix(path, "/api/v3/Image/")

	if idx := strings.Index(path, "?"); idx >= 0 {
		path = path[:idx]
	}
	if path == "" || path == imageURL {
		return imageURL
	}
	return "shoko://" + path
}

// ============================================================================
// MAIN
// ============================================================================

func main() {
	manifest, err := loadManifest()
	if err != nil {
		panic(err)
	}

	rs := &runtimeServer{
		manifest:  manifest,
		globalCfg: defaultGlobalConfig(),
	}
	ms := &metadataServer{runtime: rs}

	runtime.Serve(runtime.ServeConfig{
		Servers: runtime.CapabilityServers{
			Runtime:          rs,
			MetadataProvider: ms,
			ImageResolver:    ms,
		},
	})
}

// ============================================================================
// HELPERS
// ============================================================================

func loadManifest() (*pluginv1.PluginManifest, error) {
	manifest, err := publicmanifest.Load(manifestJSON)
	if err != nil {
		return nil, fmt.Errorf("load embedded manifest: %w", err)
	}

	if version != "" {
		manifest.Version = version
	}

	executablePath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve executable path: %w", err)
	}
	binaryData, err := os.ReadFile(executablePath)
	if err != nil {
		return nil, fmt.Errorf("read executable %q: %w", executablePath, err)
	}
	checksum := sha256.Sum256(binaryData)
	manifest.Checksum = hex.EncodeToString(checksum[:])

	return manifest, nil
}

func itemTypeForResult(providerType, requestType string) string {
	if providerType != "" {
		return providerType
	}
	return requestType
}

func stringMapFromStruct(value *structpb.Struct) map[string]string {
	result := make(map[string]string)
	if value == nil {
		return result
	}
	for key, raw := range value.AsMap() {
		text, ok := raw.(string)
		if ok && text != "" {
			result[key] = text
		}
	}
	return result
}

func stringStruct(value map[string]string) (*structpb.Struct, error) {
	if len(value) == 0 {
		return nil, nil
	}
	converted := make(map[string]any, len(value))
	for key, entry := range value {
		if entry == "" {
			continue
		}
		converted[key] = entry
	}
	if len(converted) == 0 {
		return nil, nil
	}
	return structpb.NewStruct(converted)
}
