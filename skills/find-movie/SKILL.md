---
name: find-movie
description: Find, compare, and download movies or TV episodes across private trackers using the local Torrvia service. Use when the user asks to find media, check which tracker has a release, compare resolutions or versions, or send a selected resource to the downloader. Uses search_torrents, get_torrent, list_sites, and download_torrent.
---

# Find media

Turn the user's request into targeted searches and a short list of releases they can choose from. Torrvia searches tracker data and ranks results by seeders multiplied by promotion weight. The calling AI handles title recognition, translations, and release selection.

## Workflow

1. Construct a precise keyword using the rules below.
2. Call `search_torrents`.
3. If results are empty, follow the fallback sequence before reporting no matches.
4. Narrow large result sets with filters.
5. Present 3-5 candidates and explain the recommendation.
6. Call `download_torrent` when the user explicitly requests a download.

## Search keywords

Release names commonly follow `Title.Year.Resolution.Source.Codec.Audio-Group`. Prefer the original English title and release year, even when the user uses a localized name.

| Order | Keyword | Example |
|---|---|---|
| 1 | English title and year | `Interstellar 2014` |
| 2 | English title | `Interstellar` |
| 3 | Localized title | The user's local-language title |
| 4 | Alternative regional title | A Hong Kong or Taiwan release title |

The year helps distinguish remakes, but some releases omit it. Remove the year first when no results are returned. For TV, include season or episode identifiers such as `Severance S02` or `Severance S02E01`.

## Empty-result fallback

Change one variable at a time:

1. Remove the year.
2. Remove resolution, audio, and other modifiers; keep the main title.
3. Switch between the original and localized title.
4. Shorten the title, for example `Blade Runner 2049` to `Blade Runner`.
5. Try an alternative regional title.

If every attempt fails, call `list_sites` with `check: true` to check connectivity and expired cookies. Use the same check when results are unexpectedly sparse.

## Compare releases

- **Promotion:** `free` and `2xfree` do not consume download credit; `2x` doubles upload credit. Favor freeleech when the user is concerned about ratio or account maintenance.
- **Seeders:** fewer than 5 may download slowly; avoid recommending fewer than 2 unless the user accepts the risk.
- **Size and quality:** unusually small files may be re-encodes or incomplete releases. Typical reference ranges are 8-15 GB for 1080p x264, 4-8 GB for 1080p x265, 15-30 GB for 2160p WEB-DL, and 50-80 GB for 2160p REMUX. Actual sizes vary by duration and content.
- **REMUX** preserves the original disc streams and is usually larger. **WEB-DL** is often a practical compromise.
- **`also_on`:** the service has already kept the highest-scoring copy of a resource available on multiple sites.
- **`subtitle`:** confirm localized titles, episode names, and descriptions here. The `title` field may contain only an English release name.
- **`tags`:** use site-provided subtitle and audio-language labels to confirm language requirements rather than guessing from the release name.
- **Dolby Vision:** confirm device support before recommending a DV release by default.

## Present results

Lead with the recommended release and the reason, then list a few alternatives. Use a short numbered list rather than a full results table. For example:

> Found three versions; I recommend the first:
>
> 1. **Interstellar 2160p REMUX** (62.4 GB, freeleech, 48 seeders): best quality with no download credit cost.
> 2. Interstellar 1080p BluRay (12.8 GB, freeleech + 2X, 126 seeders): smaller and well seeded.
> 3. Interstellar 2160p WEB-DL (28.6 GB, standard, 87 seeders): a balance of size and quality.
>
> Which version would you like to download?

## Download authorization

`download_torrent` consumes bandwidth and disk space and can affect the user's tracker ratio. An explicit request to download or send a release to the downloader authorizes the call. Requests about availability or comparisons authorize searches only. Ask a brief clarification when download intent is ambiguous.

## Common pitfalls

- Localized keywords can produce broad or unrelated matches. Check titles, descriptions, and tags before presenting candidates.
- Distinguish remakes such as `Dune` (1984 and 2021) or `The Thing` (1982 and 2011), using the year and release details.
- A localized title can appear only in `subtitle`, even when it is absent from `title`.
- Resource IDs refer to recently seen results. If an ID is missing, search again before retrying details or downloads.
- Trackers commonly return only one page of results. More precise queries improve coverage.
- Use only magnet links and torrent URLs returned by the tools.
