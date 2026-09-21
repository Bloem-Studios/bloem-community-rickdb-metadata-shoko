## Bloem community build

This is Bloem's community build of [RickDB/silo-plugin-metadata-shoko](https://github.com/RickDB/silo-plugin-metadata-shoko) by **RickDB**
(contributors: Quick104, RickDB, Rhainland). It is ported to the Bloem plugin SDK and listed in the
Bloem community plugin catalog. All credit for the plugin goes to its author; please
report plugin behavior issues upstream. See [NOTICE](NOTICE) for provenance.

---

# Silo Shoko Metadata Plugin

Community metadata-provider plugin for [Silo Server](https://github.com/Silo-Server/silo-server), backed by [Shoko Server](https://github.com/ShokoAnime/ShokoServer).

It maps Shoko Groups to Silo shows, child Shoko Series to seasons, and AniDB-enriched Shoko episodes to Silo episodes. Existing TMDB IDs can be used to connect an already matched Silo title to every corresponding Shoko series, allowing Shoko to enrich it with anime-specific metadata and filtered AniDB tags.

> [!IMPORTANT]
> This is a community plugin and is not maintained or endorsed by Silo Server, Shoko, AniDB, or TMDB.

## Features

- Native Shoko `/api/v3/Series/Search` title and synonym matching
- Path- and release-name-aware fallback matching for titles Silo could not cleanly parse
- Moderate per-client Shoko request limiting (10 requests/second, burst of 3)
- Shoko Group → Silo show and Shoko Series → Silo season mapping
- Movie and OVA handling independent of normal TV-season mapping
- Existing TMDB provider-ID enrichment and season/cour deduplication
- AniDB episode enrichment, including episodes without local media files
- Configurable verified-tag, spoiler-tag, and tag-weight filtering
- `shoko://` image resolution through the configured Shoko Server

## Requirements

- A compatible Silo Server installation
- Shoko Server with API v3 enabled and a populated library
- Network access from Silo to Shoko Server
- Go 1.26 or newer when building from source

## Installation

Download `plugin-linux-amd64` from the latest [GitHub release](https://github.com/RickDB/silo-plugin-metadata-shoko/releases), then install it using Silo's plugin-management interface or place it in the plugin directory expected by your Silo installation.

After installation:

1. Configure the Shoko Server base URL and API key in Silo's plugin settings.
2. Optionally configure AniDB tag filtering.
3. Add **Shoko** to the metadata-provider chain for the relevant anime library.
4. Keep TMDB ahead of Shoko when you want Shoko to enrich existing TMDB matches.

## Configuration

| Setting | Required | Description |
| --- | --- | --- |
| Shoko Server Address | Yes | Base URL reachable from Silo, for example `http://shoko:8111`. |
| API Key | Yes | Shoko API key used to authenticate API v3 requests. |
| Show Verified Tags Only | No | Imports only AniDB tags marked as verified. |
| Hide Spoiler Tags | No | Excludes AniDB tags marked as spoilers. |
| Minimum Tag Weight | No | Imports only tags at or above the selected weight. |

Credentials are supplied through Silo's plugin settings and are not hard-coded in this repository.

## Metadata model

```text
Shoko Group
└── Silo show
    ├── child Shoko Series → Silo season
    └── AniDB-enriched Shoko Episode → Silo episode
```

Normal Silo seasons include AniDB entries of type `Episode`. Credits, trailers, and other non-standard episode types are excluded. Movies and OVAs are not blindly converted into TV seasons.

## Build and test

```bash
go mod tidy
make verify
```

The local build output is `shoko_plugin`.

The `docker-copy` Make target is a developer convenience configured for a container named `silo-server`. Adjust it for your own environment before use.

## Releases

Tags matching `v*` trigger the release workflow. It builds the supported platform binary, calculates its SHA-256 checksum, and publishes both files in a GitHub Release. See [PUBLISHING.md](PUBLISHING.md) for the initial repository setup and release checklist.

## Support

Use [GitHub Issues](https://github.com/RickDB/silo-plugin-metadata-shoko/issues) for reproducible bugs and feature requests. Include the plugin version, Silo version, Shoko version, relevant logs with secrets removed, and reproduction steps.

## Attribution

This plugin integrates with Shoko Server and consumes metadata and identifiers supplied by Shoko's configured upstream sources. Shoko, AniDB, and TMDB names and marks belong to their respective owners.

## License

The current working tree declares the MIT License in [LICENSE](LICENSE). Before the first public release, verify that this is compatible with all inherited code and repository history; the project originated from the AGPL-3.0-or-later Silo TMDB plugin. See [PUBLISHING.md](PUBLISHING.md#2-resolve-the-license-before-publishing).
