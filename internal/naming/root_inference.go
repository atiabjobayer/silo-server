package naming

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

var (
	inferTitleYearRe       = regexp.MustCompile(`^(.+?)\s*\((\d{4})\)`)
	inferWhitespaceTokenRe = regexp.MustCompile(`\s+`)
	inferReleaseTokenRe    = regexp.MustCompile(`(?i)\b(?:remux|bluray|bdrip|brrip|web[ ._-]?dl|webrip|hdr|dv|2160p|1080p|720p|x264|x265|h\.?264|h\.?265|hevc|av1|aac|dts|truehd|atmos|eac3|ac3|flac|opus|mp3|ddp?5\.1|ddp?7\.1|ddp?2\.0)\b`)
	inferSeasonEpisodeRe   = regexp.MustCompile(`(?i)[Ss](\d{1,4})[Ee](\d{1,3})`)
	inferSeasonEpisodeXRe  = regexp.MustCompile(`(?i)(?:^|[^0-9])(\d{1,4})\s*[x×]\s*(\d{1,3})(?:[^0-9]|$)`)
	inferSeasonDirRe       = regexp.MustCompile(`(?i)^Season\s+(\d{1,4})(?:\s.*)?$`)
	inferNumericSeasonRe   = regexp.MustCompile(`^\d{1,4}$`)
	inferSpecialsDirRe     = regexp.MustCompile(`(?i)^(?:specials?|extras?)$`)
	// Matches a well-formed tag ([tvdb-81189]), an unsubstituted Sonarr token
	// ([tvdb-{TvdbId}]), or an empty token ({imdb-}). The id part is either a
	// {...} placeholder or zero-or-more word chars.
	inferProviderTagRe = regexp.MustCompile(`\s*[{\[](?:tmdb|tmdbid|imdb|imdbid|tvdb|tvdbid)-(?:\{[^}]*\}|[\w]*)[}\]]`)

	// ----- title cleaning regexes: strip release cruft before metadata matching -----

	// Square-bracketed tags: [Dual Audio], [Hindi 5.1+English 5.1],
	// [Hindi (Clean)+English CAM], [DUAL-AUDIO], [ESub], etc.
	inferBracketedTagRe = regexp.MustCompile(`\s*\[[^\]]*(?i)(?:dual[ ._-]?audio|multi[ ._-]?audio|hindi|english|tamil|telugu|malayalam|kannada|bengali|punjabi|gujarati|marathi|esub|subs?)(?:[^\]]*)\]`)

	// Parenthetical year ranges: (TV Series 2009–2010), (2009–2010),
	// (2009 - 2010), (TV Series 2009-10), (TV Series 2024–),
	// (TV Mini Series 2016), etc. Must NOT match plain (2008) —
	// that is a useful year that parseInferFolderTitleYear needs.
	// Requires either a "TV Series"/"Mini Series" prefix or a dash.
	inferYearRangeRe = regexp.MustCompile(`\s*\((?:TV\s*(?:Mini[ ._-]?\s*)?Series\s*)\d{4}(?:\s*[–\-]\s*(?:\d{2,4})?(?:\s*[–\-])?)?\)|\s*\(\d{4}\s*[–\-]\s*(?:\d{2,4})?\)`)

	// Trailing release group and scene suffixes. Anchored to end of string.
	// Examples: -HDHub, -MsMod, -mkvC, -RARBG, -ION10, -DDR, -Telly,
	//           -WARRNING (sic), -EZTV, -mSD, -minx, -ETHEL.
	// Also catches -[scene] and .[scene] variants (case-insensitive).
	inferReleaseSuffixRe = regexp.MustCompile(`(?i)[ .\-–](?:hdhub|msmod|mkvc|rarbg|ion\d*|ddr|telly|war?r?ning|eztv|msd|minx|ethel|megusta|galaxyrg|psa|yify|yts|cmrg|crazy|sadece|avi|evo|hive|mzabi|vyndros|flux|fgt|saint|samppa|mundane|tigole|prof|mteam|ntb|ntg|tps|ebp|playnow|fleet|phoenix|mzabi|smurf|emx|tigole)\b[ .\-–]*$`)

	// Source/quality/edition tokens that are NOT covered by
	// inferReleaseTokenRe (that regex requires word-boundary isolation).
	// Covers compound tokens like "HDTC", "AMZN", "WEBRip", "HC-HDRip",
	// and edition markers like "Unrated", "Extended", "Director's Cut".
	inferSourceQualityRe = regexp.MustCompile(`(?i)\b(?:hdtc|hdcam|amzn|web[ ._-]?rip|hmax|dsnp|nf|atvp|hulu|peacock|hc[ ._-]?hdrip|unrated|extended|directors?[ ._-]?cut|theatrical|special[ ._-]?edition|remastered|imax|open[ ._-]?matte)\b`)

	// Version/part markers: V2, V3, PART 1, CD1, etc.
	inferVersionPartRe = regexp.MustCompile(`(?i)\b(?:v\d+|part[ ._-]?\d+|cd\d+|disc[ ._-]?\d+|proper|repack|rerip|internal|limited|readnfo)\b`)
)

type RootAssignment struct {
	FilePath               string
	RootPath               string
	InferredType           string
	Title                  string
	Year                   int
	LegacyRootPath         string
	LegacyType             string
	HasFolderIDs           bool
	HasSeasonStructure     bool
	HasMovieEvidence       bool
	HasEpisodePattern      bool
	WrapperCollapsed       bool
	PromotedAncestor       bool
	HasStrongContradiction bool
}

func InferRootAssignments(
	filePaths []string,
	libraryType string,
	folderID int,
	overrides map[string]models.MediaRootOverride,
) ([]models.ScannedMediaRoot, map[string]RootAssignment) {
	assignments := make(map[string]RootAssignment, len(filePaths))
	if len(filePaths) == 0 {
		return []models.ScannedMediaRoot{}, assignments
	}

	type aggregate struct {
		root                 models.ScannedMediaRoot
		hasFolderIDs         bool
		wrapperCollapses     int
		ancestorPromotions   int
		legacyDisagreements  int
		seasonEvidenceCount  int
		episodeEvidenceCount int
		movieEvidenceCount   int
		releaseDensityCount  int
		movieTypeVotes       int
		seriesTypeVotes      int
		contradictionCount   int
	}

	byRoot := make(map[string]*aggregate, len(filePaths))
	for _, rawPath := range filePaths {
		cleanFilePath := filepath.Clean(rawPath)
		assignment := inferFileRootAssignment(cleanFilePath, libraryType, overrides)
		assignments[cleanFilePath] = assignment

		agg, found := byRoot[assignment.RootPath]
		if !found {
			agg = &aggregate{
				root: models.ScannedMediaRoot{
					MediaFolderID:     folderID,
					RootPath:          assignment.RootPath,
					State:             "resolved",
					InferredType:      assignment.InferredType,
					TypeConfidence:    "low",
					ObservedFileCount: 0,
					SampleFilePath:    cleanFilePath,
					OverrideSource:    "none",
					Title:             assignment.Title,
					Year:              assignment.Year,
				},
			}
			if ids := ParseFolderIDs(filepath.Base(assignment.RootPath)); ids != nil {
				agg.hasFolderIDs = true
				agg.root.TmdbID = ids.TmdbID
				agg.root.ImdbID = ids.ImdbID
				agg.root.TvdbID = ids.TvdbID
			}
			byRoot[assignment.RootPath] = agg
		}

		agg.root.ObservedFileCount++
		agg.hasFolderIDs = agg.hasFolderIDs || assignment.HasFolderIDs
		if assignment.WrapperCollapsed {
			agg.wrapperCollapses++
		}
		if assignment.PromotedAncestor {
			agg.ancestorPromotions++
		}
		if assignment.LegacyRootPath != "" && assignment.LegacyRootPath != assignment.RootPath {
			agg.legacyDisagreements++
		}
		if assignment.HasSeasonStructure {
			agg.seasonEvidenceCount++
		}
		if assignment.HasEpisodePattern {
			agg.episodeEvidenceCount++
		}
		if assignment.HasMovieEvidence {
			agg.movieEvidenceCount++
		}
		if assignment.HasStrongContradiction {
			agg.contradictionCount++
		}
		if inferReleaseTokenRe.MatchString(filepath.Base(assignment.RootPath)) {
			agg.releaseDensityCount++
		}
		switch assignment.InferredType {
		case "series":
			agg.seriesTypeVotes++
		default:
			agg.movieTypeVotes++
		}
		if agg.root.Title == "" && assignment.Title != "" {
			agg.root.Title = assignment.Title
		}
		if agg.root.Year == 0 && assignment.Year != 0 {
			agg.root.Year = assignment.Year
		}
	}

	roots := make([]string, 0, len(byRoot))
	for rootPath := range byRoot {
		roots = append(roots, rootPath)
	}
	sort.Strings(roots)

	snapshots := make([]models.ScannedMediaRoot, 0, len(roots))
	for _, rootPath := range roots {
		agg := byRoot[rootPath]

		switch {
		case agg.seriesTypeVotes > 0 && agg.movieTypeVotes == 0:
			agg.root.InferredType = "series"
		case agg.movieTypeVotes > 0 && agg.seriesTypeVotes == 0:
			agg.root.InferredType = "movie"
		case agg.seasonEvidenceCount > 0 || agg.episodeEvidenceCount > agg.movieEvidenceCount:
			agg.root.InferredType = "series"
		default:
			agg.root.InferredType = "movie"
		}

		switch {
		case agg.hasFolderIDs || agg.seasonEvidenceCount > 0 || agg.movieEvidenceCount > 0:
			agg.root.TypeConfidence = "high"
		case agg.root.Title != "" || agg.root.Year != 0 || agg.episodeEvidenceCount > 0 || agg.root.ObservedFileCount > 1:
			agg.root.TypeConfidence = "medium"
		default:
			agg.root.TypeConfidence = "low"
		}

		conflictingTypeVotes := agg.seriesTypeVotes > 0 && agg.movieTypeVotes > 0 &&
			(agg.seasonEvidenceCount > 0 || agg.episodeEvidenceCount > 0) &&
			agg.movieEvidenceCount > 0
		if conflictingTypeVotes ||
			(!agg.hasFolderIDs && agg.root.TypeConfidence == "low" && agg.root.Title == "") ||
			(agg.contradictionCount > 0 && agg.root.ObservedFileCount == 1) {
			agg.root.State = "ambiguous"
		}

		if override, ok := overrides[rootPath]; ok {
			agg.root.OverrideSource = "manual"
			agg.root.State = "resolved"
			if override.ForcedType != "" {
				agg.root.InferredType = override.ForcedType
			}
			if override.ForcedTitle != "" {
				agg.root.Title = override.ForcedTitle
			}
			if override.ForcedYear != 0 {
				agg.root.Year = override.ForcedYear
			}
			if override.ForcedTmdbID != "" {
				agg.root.TmdbID = override.ForcedTmdbID
			}
			if override.ForcedImdbID != "" {
				agg.root.ImdbID = override.ForcedImdbID
			}
			if override.ForcedTvdbID != "" {
				agg.root.TvdbID = override.ForcedTvdbID
			}
		}

		evidence, _ := json.Marshal(map[string]any{
			"has_folder_ids":         agg.hasFolderIDs,
			"season_structure_files": agg.seasonEvidenceCount,
			"episode_pattern_files":  agg.episodeEvidenceCount,
			"movie_evidence_files":   agg.movieEvidenceCount,
			"release_density_files":  agg.releaseDensityCount,
			"wrapper_collapses":      agg.wrapperCollapses,
			"ancestor_promotions":    agg.ancestorPromotions,
			"legacy_disagreements":   agg.legacyDisagreements,
			"movie_type_votes":       agg.movieTypeVotes,
			"series_type_votes":      agg.seriesTypeVotes,
			"override_applied":       agg.root.OverrideSource == "manual",
			"observed_file_count":    agg.root.ObservedFileCount,
			"contradiction_files":    agg.contradictionCount,
		})
		agg.root.EvidenceJSON = evidence
		snapshots = append(snapshots, agg.root)
	}

	return snapshots, assignments
}

func inferFileRootAssignment(
	filePath string,
	libraryType string,
	overrides map[string]models.MediaRootOverride,
) RootAssignment {
	assignment := extractPathEvidence(filePath, libraryType)

	if overrideRoot, ok := deepestOverrideAncestor(filePath, overrides); ok {
		assignment.RootPath = overrideRoot
		assignment.PromotedAncestor = overrideRoot != assignment.LegacyRootPath
	}

	promotedRoot, wrapperCollapsed, promotedAncestor := promoteCandidateRoot(
		filePath,
		assignment.RootPath,
		assignment.Title,
		assignment.Year,
	)
	assignment.RootPath = promotedRoot
	assignment.WrapperCollapsed = wrapperCollapsed
	assignment.PromotedAncestor = assignment.PromotedAncestor || promotedAncestor

	if assignment.Title == "" || assignment.Year == 0 {
		rootTitle, rootYear := parseInferTitleYear(filepath.Base(assignment.RootPath))
		if assignment.Title == "" {
			assignment.Title = rootTitle
		}
		if assignment.Year == 0 {
			assignment.Year = rootYear
		}
	}

	if ids := ParseFolderIDs(filepath.Base(assignment.RootPath)); ids != nil {
		assignment.HasFolderIDs = true
	}

	return assignment
}

func extractPathEvidence(filePath string, libraryType string) RootAssignment {
	cleanFilePath := filepath.Clean(filePath)
	baseName := filepath.Base(cleanFilePath)
	nameNoExt := strings.TrimSuffix(baseName, filepath.Ext(baseName))
	parentDir := filepath.Dir(cleanFilePath)
	parentBase := filepath.Base(parentDir)
	pathParts := strings.Split(filepath.ToSlash(cleanFilePath), "/")
	dirParts := pathParts[:max(len(pathParts)-1, 0)]

	hasEpisodePattern := inferSeasonEpisodeRe.MatchString(nameNoExt) || inferSeasonEpisodeXRe.MatchString(nameNoExt)
	hasSeasonStructure := detectInferSeasonStructure(dirParts, hasEpisodePattern || normalizeInferLibraryType(libraryType) == "series")
	parentTitle, parentYear, parentTrusted := parseInferFolderTitleYear(parentBase)
	fileStem := parseInferMovieStem(nameNoExt, parentTitle, parentYear)
	hasMovieEvidence := detectInferMovieFolderEvidence(parentBase, nameNoExt, hasSeasonStructure)
	strongMovieContradiction := false
	if !hasSeasonStructure && parentTrusted && fileStem.Title != "" && !inferTitlesCoherent(parentTitle, fileStem.Title) {
		strongMovieContradiction = true
	}
	if !hasSeasonStructure && parentTrusted && hasEpisodePattern {
		strongMovieContradiction = true
	}

	inferredType := normalizeInferLibraryType(libraryType)
	switch inferredType {
	case "series":
		inferredType = "series"
	case "movie":
		inferredType = "movie"
	default:
		switch {
		case hasSeasonStructure:
			inferredType = "series"
		case hasMovieEvidence:
			inferredType = "movie"
		case hasEpisodePattern:
			inferredType = "series"
		default:
			inferredType = "movie"
		}
	}

	rootPath := parentDir
	if inferredType == "series" {
		rootPath = deriveInferSeriesRootPath(cleanFilePath, dirParts)
	} else {
		rootPath = deriveInferMovieRootPath(cleanFilePath, hasMovieEvidence || parentTrusted)
	}

	title, year := parseInferTitleYear(filepath.Base(rootPath))
	if inferredType == "movie" && parentTrusted {
		title = parentTitle
		year = parentYear
	}
	if title == "" || year == 0 {
		fileTitle, fileYear := parseInferTitleYear(nameNoExt)
		if title == "" {
			title = fileTitle
		}
		if year == 0 {
			year = fileYear
		}
	}

	return RootAssignment{
		FilePath:               cleanFilePath,
		RootPath:               filepath.Clean(rootPath),
		InferredType:           inferredType,
		Title:                  title,
		Year:                   year,
		LegacyRootPath:         filepath.Clean(rootPath),
		LegacyType:             inferredType,
		HasSeasonStructure:     hasSeasonStructure,
		HasMovieEvidence:       hasMovieEvidence,
		HasEpisodePattern:      hasEpisodePattern,
		HasStrongContradiction: strongMovieContradiction,
	}
}

func normalizeInferLibraryType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "movie", "movies":
		return "movie"
	case "series", "tv", "show", "tvshows":
		return "series"
	default:
		return ""
	}
}

func detectInferSeasonStructure(parts []string, allowNumeric bool) bool {
	for _, part := range parts {
		segment := filepath.Base(part)
		switch {
		case inferSeasonDirRe.MatchString(segment):
			return true
		case allowNumeric && inferNumericSeasonRe.MatchString(segment):
			return true
		case inferSpecialsDirRe.MatchString(segment):
			return true
		}
	}
	return false
}

// IsMisplacedSeriesFile reports whether filePath is a TV episode that lives
// inside an explicit "Season NN/" (or "Specials"/"Extras") directory. Such a
// file is a series structure dropped into a movie-type library (e.g. a fan
// "supercuts" pack): a movie library would otherwise turn every episode into a
// bogus per-episode "Season NN" movie that never matches a movie provider.
//
// The check is intentionally strict — it requires BOTH an SxxExx pattern in the
// file name AND an explicit season/specials ancestor directory — so legitimate
// movies that merely carry an episode-like substring in their release name
// (e.g. "...S01E43..." inside a "Title (Year)/" folder) are never flagged.
func IsMisplacedSeriesFile(filePath string) bool {
	clean := filepath.Clean(filePath)
	baseName := filepath.Base(clean)
	nameNoExt := strings.TrimSuffix(baseName, filepath.Ext(baseName))
	if !(inferSeasonEpisodeRe.MatchString(nameNoExt) || inferSeasonEpisodeXRe.MatchString(nameNoExt)) {
		return false
	}
	parts := strings.Split(filepath.ToSlash(clean), "/")
	for _, part := range parts[:max(len(parts)-1, 0)] {
		if inferSeasonDirRe.MatchString(part) || inferSpecialsDirRe.MatchString(part) {
			return true
		}
	}
	return false
}

func detectInferMovieFolderEvidence(parentBase string, nameNoExt string, hasSeasonStructure bool) bool {
	if hasSeasonStructure {
		return false
	}
	if ParseFolderIDs(parentBase) != nil {
		return true
	}
	parentTitle, parentYear, trusted := parseInferFolderTitleYear(parentBase)
	if parentTitle == "" || (!trusted && parentYear == 0) {
		return false
	}
	fileStem := parseInferMovieStem(nameNoExt, parentTitle, parentYear)
	if fileStem.Title == "" {
		return false
	}
	if !inferTitlesCoherent(parentTitle, fileStem.Title) {
		return false
	}
	if parentYear != 0 && fileStem.Year != 0 && parentYear != fileStem.Year {
		return true
	}
	return true
}

func deriveInferSeriesRootPath(filePath string, dirParts []string) string {
	if len(dirParts) == 0 {
		return filepath.Dir(filePath)
	}
	for i := len(dirParts) - 1; i >= 0; i-- {
		segment := filepath.Base(dirParts[i])
		if inferSeasonDirRe.MatchString(segment) || inferNumericSeasonRe.MatchString(segment) || inferSpecialsDirRe.MatchString(segment) {
			if i > 0 {
				return filepath.Clean(strings.Join(dirParts[:i], string(filepath.Separator)))
			}
			return filepath.Dir(filePath)
		}
	}
	return filepath.Dir(filePath)
}

func deriveInferMovieRootPath(filePath string, hasMovieEvidence bool) string {
	parentDir := filepath.Dir(filePath)
	if hasMovieEvidence {
		return parentDir
	}
	baseName := filepath.Base(filePath)
	nameNoExt := strings.TrimSuffix(baseName, filepath.Ext(baseName))
	return filepath.Join(parentDir, nameNoExt)
}

func deepestOverrideAncestor(
	filePath string,
	overrides map[string]models.MediaRootOverride,
) (string, bool) {
	if len(overrides) == 0 {
		return "", false
	}

	cleanFilePath := filepath.Clean(filePath)
	longest := ""
	for rootPath := range overrides {
		cleanRoot := filepath.Clean(rootPath)
		rel, err := filepath.Rel(cleanRoot, cleanFilePath)
		if err != nil {
			continue
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if len(cleanRoot) > len(longest) {
			longest = cleanRoot
		}
	}
	if longest == "" {
		return "", false
	}
	return longest, true
}

func promoteCandidateRoot(filePath, currentRoot, title string, year int) (string, bool, bool) {
	cleanRoot := filepath.Clean(currentRoot)
	wrapperCollapsed := false
	promotedAncestor := false

	for {
		parent := filepath.Dir(cleanRoot)
		if parent == "." || parent == "/" || parent == cleanRoot || parent == "" {
			break
		}

		parentBase := filepath.Base(parent)
		currentBase := filepath.Base(cleanRoot)
		parentIDs := ParseFolderIDs(parentBase) != nil
		childTitle, childYear := parseInferTitleYear(currentBase)
		parentTitle, parentYear := parseInferTitleYear(parentBase)

		sameNamedWrapper := sameInferRootIdentity(parentTitle, parentYear, childTitle, childYear)
		matchesParsedTitle := sameInferRootIdentity(parentTitle, parentYear, title, year)
		legacySyntheticChild := normalizeInferComparable(currentBase) == normalizeInferComparable(strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath)))

		switch {
		case sameNamedWrapper:
			cleanRoot = parent
			wrapperCollapsed = true
			promotedAncestor = true
			continue
		case parentIDs && (matchesParsedTitle || legacySyntheticChild):
			cleanRoot = parent
			promotedAncestor = true
			continue
		default:
			return cleanRoot, wrapperCollapsed, promotedAncestor
		}
	}

	return cleanRoot, wrapperCollapsed, promotedAncestor
}

func sameInferRootIdentity(aTitle string, aYear int, bTitle string, bYear int) bool {
	if normalizeInferComparable(aTitle) == "" || normalizeInferComparable(bTitle) == "" {
		return false
	}
	if !inferTitlesCoherent(aTitle, bTitle) {
		return false
	}
	if aYear != 0 && bYear != 0 && aYear != bYear {
		return false
	}
	return true
}

func parseInferTitleYear(name string) (string, int) {
	surface := stripInferProviderTags(name)
	surface = inferWhitespaceTokenRe.ReplaceAllString(surface, " ")
	surface = strings.TrimSpace(surface)
	if surface == "" {
		return "", 0
	}
	// Strip release cruft before attempting structured year extraction.
	cleaned := stripInferReleaseTokens(surface)
	if cleaned != "" {
		surface = cleaned
	}
	if title, year, trusted := parseInferFolderTitleYear(surface); trusted {
		return title, year
	}
	if match := inferTitleYearRe.FindStringSubmatch(surface); match != nil {
		year := 0
		for _, r := range match[2] {
			year = year*10 + int(r-'0')
		}
		return strings.TrimSpace(match[1]), year
	}
	if stem := parseInferMovieStem(surface, "", 0); stem.Title != "" && stem.Year != 0 {
		return stem.Title, stem.Year
	}
	return surface, 0
}

func stripInferProviderTags(name string) string {
	return strings.TrimSpace(inferProviderTagRe.ReplaceAllString(name, " "))
}

// stripInferReleaseTokens removes common release-related cruft from folder
// names so metadata provider matching sees a clean title. Applied before
// structured year extraction in parseInferTitleYear.
func stripInferReleaseTokens(name string) string {
	s := name

	// 1. Strip square-bracketed language/audio tags: [Dual Audio],
	//    [Hindi 5.1+English 5.1], [ESub], etc.
	s = inferBracketedTagRe.ReplaceAllString(s, " ")

	// 2. Strip parenthetical year ranges: (TV Series 2009–2010), (2009-10).
	s = inferYearRangeRe.ReplaceAllString(s, " ")

	// 3. Strip trailing release group and scene suffixes: -HDHub, -RARBG, etc.
	s = inferReleaseSuffixRe.ReplaceAllString(s, "")

	// 4. Strip source/quality tokens: HDTC, AMZN, Unrated, etc.
	s = inferSourceQualityRe.ReplaceAllString(s, " ")

	// 5. Strip version/part markers: V2, PROPER, CD1, etc.
	s = inferVersionPartRe.ReplaceAllString(s, " ")

	// 6. Strip standalone technical tokens (codecs, resolutions, etc.)
	s = inferReleaseTokenRe.ReplaceAllString(s, " ")

	// Collapse resulting whitespace.
	s = collapseWhitespace(strings.TrimSpace(s))

	if s == "" || s == name {
		return ""
	}
	return s
}
