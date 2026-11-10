package index

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/analysis/analyzer/keyword"
	"github.com/blevesearch/bleve/v2/analysis/analyzer/standard"
	"github.com/blevesearch/bleve/v2/mapping"
	bquery "github.com/blevesearch/bleve/v2/search/query"
	"go.uber.org/zap"

	"github.com/slopster67/mcpproxy-go/internal/config"
)

// searchPageSize is the page size used when paginating full-coverage scans
// (GetToolsByServer, DeleteAll). Pagination loops over as many pages as
// needed, so total coverage is never bounded by this value.
const searchPageSize = 10000

const (
	// maxUnderscoreSearchSegments bounds the extra wildcard clauses generated for
	// identifier-style queries. Typical MCP tool names stay well below this cap.
	maxUnderscoreSearchSegments = 16
	underscoreSegmentBoost      = 5.0

	// bareSegmentMinLen is the shortest single-token query that also matches
	// as an underscore-delimited tool-name segment; shorter tokens would make
	// the wildcard scan too broad and noisy. bareSegmentBoost stays below the
	// exact (5.0) and prefix (3.0) tool_name clauses so exact and prefix hits
	// stay on top.
	bareSegmentMinLen = 3
	bareSegmentBoost  = 1.5
)

// BleveIndex wraps Bleve index operations
type BleveIndex struct {
	index  bleve.Index
	logger *zap.Logger
}

// ToolDocument represents a tool document in the index
type ToolDocument struct {
	ToolName         string `json:"tool_name"`      // Just the tool name (without server prefix)
	FullToolName     string `json:"full_tool_name"` // Complete server:tool format
	ServerName       string `json:"server_name"`
	Description      string `json:"description"`
	ParamsJSON       string `json:"params_json"`
	OutputSchemaJSON string `json:"output_schema_json,omitempty"`
	Hash             string `json:"hash"`
	Tags             string `json:"tags"`
	SearchableText   string `json:"searchable_text"` // Combined searchable content
	// AnnotationsJSON is the tool's MCP behavior-hint annotations
	// (config.ToolAnnotations), marshaled once at index time. Stored but not
	// indexed for search, same as Hash — it exists so a search hit's tier
	// reflects the tool's real annotations and agrees with the same tool's
	// tier on GET /servers/{id}/tools.
	AnnotationsJSON string `json:"annotations_json,omitempty"`
}

// NewBleveIndex creates (or opens) the shared default Bleve index at
// <dataDir>/index.bleve.
func NewBleveIndex(dataDir string, logger *zap.Logger) (*BleveIndex, error) {
	return newBleveIndexAt(filepath.Join(dataDir, "index.bleve"), logger)
}

func newBleveIndexAt(indexPath string, logger *zap.Logger) (*BleveIndex, error) {
	var index bleve.Index
	var err error
	if _, statErr := os.Stat(indexPath); errors.Is(statErr, os.ErrNotExist) {
		if err = os.MkdirAll(filepath.Dir(indexPath), 0o755); err == nil {
			index, err = createBleveIndex(indexPath)
		}
	} else if statErr != nil {
		return nil, statErr
	} else {
		index, err = bleve.Open(indexPath)
	}
	if err != nil {
		return nil, fmt.Errorf("open search index %s: %w", indexPath, err)
	}
	return &BleveIndex{index: index, logger: logger}, nil
}

// createBleveIndex creates a new, empty Bleve index with the current mapping.
func createBleveIndex(indexPath string) (bleve.Index, error) {
	idx, err := bleve.New(indexPath, currentIndexMapping())
	if err != nil {
		return nil, err
	}
	return idx, nil
}

func currentIndexMapping() *mapping.IndexMappingImpl {
	indexMapping := bleve.NewIndexMapping()

	// Create document mapping for tools
	toolMapping := bleve.NewDocumentMapping()
	toolMapping.Dynamic = false

	// Tool name field (both keyword and standard analyzers for different search types)
	toolNameFieldKeyword := bleve.NewTextFieldMapping()
	toolNameFieldKeyword.Analyzer = keyword.Name
	toolNameFieldKeyword.Store = true
	toolNameFieldKeyword.Index = true
	toolMapping.AddFieldMappingsAt("tool_name", toolNameFieldKeyword)

	// Full tool name field (keyword analyzer for exact matches)
	fullToolNameField := bleve.NewTextFieldMapping()
	fullToolNameField.Analyzer = keyword.Name
	fullToolNameField.Store = true
	fullToolNameField.Index = true
	toolMapping.AddFieldMappingsAt("full_tool_name", fullToolNameField)

	// Server name field (keyword analyzer)
	serverNameField := bleve.NewTextFieldMapping()
	serverNameField.Analyzer = keyword.Name
	serverNameField.Store = true
	serverNameField.Index = true
	toolMapping.AddFieldMappingsAt("server_name", serverNameField)

	// Description field (standard analyzer for full-text search)
	descriptionField := bleve.NewTextFieldMapping()
	descriptionField.Analyzer = standard.Name
	descriptionField.Store = true
	descriptionField.Index = true
	toolMapping.AddFieldMappingsAt("description", descriptionField)

	// Parameters JSON field (standard analyzer)
	paramsField := bleve.NewTextFieldMapping()
	paramsField.Analyzer = standard.Name
	paramsField.Store = true
	paramsField.Index = true
	toolMapping.AddFieldMappingsAt("params_json", paramsField)

	// Output schema JSON field: stored only, never searched (it is JSON, not
	// prose) — same shape as hash and annotations_json below. The explicit
	// mapping is required because toolMapping.Dynamic is false: without it the
	// field would not be stored for retrieval.
	outputSchemaField := bleve.NewTextFieldMapping()
	outputSchemaField.Analyzer = keyword.Name
	outputSchemaField.Store = true
	outputSchemaField.Index = false
	toolMapping.AddFieldMappingsAt("output_schema_json", outputSchemaField)

	// Hash field (keyword analyzer)
	hashField := bleve.NewTextFieldMapping()
	hashField.Analyzer = keyword.Name
	hashField.Store = true
	hashField.Index = false // Don't index hash for search
	toolMapping.AddFieldMappingsAt("hash", hashField)

	// Annotations field: stored only, never searched (it is JSON, not prose).
	annotationsField := bleve.NewTextFieldMapping()
	annotationsField.Analyzer = keyword.Name
	annotationsField.Store = true
	annotationsField.Index = false
	toolMapping.AddFieldMappingsAt("annotations_json", annotationsField)

	// Tags field (standard analyzer)
	tagsField := bleve.NewTextFieldMapping()
	tagsField.Analyzer = standard.Name
	tagsField.Store = true
	tagsField.Index = true
	toolMapping.AddFieldMappingsAt("tags", tagsField)

	// Searchable text field (standard analyzer) - combines all searchable content
	searchableTextField := bleve.NewTextFieldMapping()
	searchableTextField.Analyzer = standard.Name
	searchableTextField.Store = false // Don't store, just index for search
	searchableTextField.Index = true
	toolMapping.AddFieldMappingsAt("searchable_text", searchableTextField)

	// Add document mapping to index
	indexMapping.AddDocumentMapping("tool", toolMapping)
	indexMapping.DefaultMapping = toolMapping

	return indexMapping
}

// Close closes the index
func (b *BleveIndex) Close() error {
	return b.index.Close()
}

func toolDocument(toolMeta *config.ToolMetadata) (string, *ToolDocument) {
	toolName := config.RawToolName(toolMeta)
	docID := toolDocID(toolMeta.ServerName, toolName)

	var annotationsJSON string
	if toolMeta.Annotations != nil {
		if b, err := json.Marshal(toolMeta.Annotations); err == nil {
			annotationsJSON = string(b)
		}
		// A marshal error here is unreachable for config.ToolAnnotations (plain
		// strings/bools/pointers, no cyclic or unsupported types) — silently
		// falling back to "no annotations stored" rather than failing the whole
		// index write matches how the rest of this function tolerates partial
		// metadata (e.g. an empty OutputSchemaJSON).
	}

	doc := &ToolDocument{
		ToolName:         toolName,
		FullToolName:     toolMeta.Name,
		ServerName:       toolMeta.ServerName,
		Description:      toolMeta.Description,
		ParamsJSON:       toolMeta.ParamsJSON,
		OutputSchemaJSON: toolMeta.OutputSchemaJSON,
		Hash:             toolMeta.Hash,
		Tags:             "", // Can be extended later
		SearchableText:   searchableText(toolName, toolMeta.Name, toolMeta.Description, toolMeta.ParamsJSON),
		AnnotationsJSON:  annotationsJSON,
	}

	return docID, doc
}

// searchableText is the combined full-text field (indexed, not stored).
func searchableText(toolName, fullToolName, description, paramsJSON string) string {
	return fmt.Sprintf("%s %s %s %s", toolName, fullToolName, description, paramsJSON)
}

// toolDocID is the single place the "<server>:<raw name>" docID is spelled, so
// BatchIndex and DeleteTool can never disagree on a tool's identity.
func toolDocID(serverName, rawName string) string {
	return config.CanonicalToolName(serverName, rawName)
}

// readToolMetadata rebuilds tool metadata from a stored hit. Identity comes
// from the docID alone: the docID is "<server>:<raw name>", so the canonical
// Name IS the docID and RawName is the docID with exactly this server's own
// prefix trimmed once — never re-derived from the stored tool_name or
// full_tool_name fields, which are search fields, not identity. A raw name
// that begins with the server's own prefix ("a:erase" on server "a", docID
// "a:a:erase") therefore keeps its exact identity.
func readToolMetadata(docID string, fields map[string]interface{}) *config.ToolMetadata {
	serverName := getStringField(fields, "server_name")
	canonical := docID
	var annotations *config.ToolAnnotations
	if raw := getStringField(fields, "annotations_json"); raw != "" {
		var parsed config.ToolAnnotations
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
			annotations = &parsed
		}
		// A malformed stored value (should not happen; toolDocument only ever
		// writes what json.Marshal produced) falls back to nil — TierUnannotated
		// — rather than surfacing a decode error through every search result.
	}

	return &config.ToolMetadata{
		Name:             canonical,
		RawName:          strings.TrimPrefix(canonical, serverName+":"),
		ServerName:       serverName,
		Description:      getStringField(fields, "description"),
		ParamsJSON:       getStringField(fields, "params_json"),
		OutputSchemaJSON: getStringField(fields, "output_schema_json"),
		Hash:             getStringField(fields, "hash"),
		Annotations:      annotations,
	}
}

// DeleteTool removes a tool from the index, addressed by its exact raw name.
func (b *BleveIndex) DeleteTool(serverName, toolName string) error {
	docID := toolDocID(serverName, toolName)

	b.logger.Debug("Deleting tool from index", zap.String("doc_id", docID))
	return b.index.Delete(docID)
}

// DeleteServerTools removes all tools from a specific server
func (b *BleveIndex) DeleteServerTools(serverName string) error {
	query := bleve.NewTermQuery(serverName)
	query.SetField("server_name")

	deleted, err := b.deleteMatching(query, "failed to search for server tools", "failed to delete server tools")
	if err != nil {
		return err
	}

	b.logger.Info("Deleted tools from server",
		zap.Int("count", deleted),
		zap.String("server", serverName))
	return nil
}

// buildToolSearchQuery is the ONE boolean query both SearchTools and
// SearchToolsScoped run: every clause is a Should, so
// matching is boolean and corpus-independent, and no scoped variant may add
// a Must term on server_name — that would change BM25 scores, and the scoped
// path must return the unfiltered search's scores for the hits it keeps.
func buildToolSearchQuery(queryStr string) *bquery.BooleanQuery {
	// Create a boolean query to combine multiple search strategies
	boolQuery := bleve.NewBooleanQuery()

	// 1. Exact match on tool name (highest priority)
	exactToolNameQuery := bleve.NewTermQuery(queryStr)
	exactToolNameQuery.SetField("tool_name")
	exactToolNameQuery.SetBoost(5.0)
	boolQuery.AddShould(exactToolNameQuery)

	// 2. Exact match on full tool name
	exactFullToolNameQuery := bleve.NewTermQuery(queryStr)
	exactFullToolNameQuery.SetField("full_tool_name")
	exactFullToolNameQuery.SetBoost(4.0)
	boolQuery.AddShould(exactFullToolNameQuery)

	// 3. Prefix match on tool name for partial matches
	prefixToolNameQuery := bleve.NewPrefixQuery(queryStr)
	prefixToolNameQuery.SetField("tool_name")
	prefixToolNameQuery.SetBoost(3.0)
	boolQuery.AddShould(prefixToolNameQuery)

	// 4. Wildcard search for underscore-separated terms
	if strings.Contains(queryStr, "_") {
		wildcardQuery := bleve.NewWildcardQuery("*" + queryStr + "*")
		wildcardQuery.SetField("tool_name")
		wildcardQuery.SetBoost(2.5)
		boolQuery.AddShould(wildcardQuery)
	}

	// 5. Full-text search across all fields
	matchQuery := bleve.NewMatchQuery(queryStr)
	matchQuery.SetBoost(1.0)
	boolQuery.AddShould(matchQuery)

	// 6. Search in combined searchable text
	searchableTextQuery := bleve.NewMatchQuery(queryStr)
	searchableTextQuery.SetField("searchable_text")
	searchableTextQuery.SetBoost(1.5)
	boolQuery.AddShould(searchableTextQuery)

	return boolQuery
}

// newToolSearchRequest builds the ranked-window request SearchTools and
// SearchToolsScoped share: the same fields, highlight and the deterministic
// score-then-id sort, over the window [from, from+size).
func newToolSearchRequest(q bquery.Query, from, size int) *bleve.SearchRequest {
	searchReq := bleve.NewSearchRequest(q)
	searchReq.From = from
	searchReq.Size = size
	searchReq.Fields = []string{"tool_name", "full_tool_name", "server_name", "description", "params_json", "output_schema_json", "hash", "annotations_json"}
	searchReq.Highlight = bleve.NewHighlight()

	// Deterministic tie-break: primary sort by score descending (bleve's
	// default), secondary by document ID (server:tool) ascending. Without the
	// secondary key, equally-scored hits come back in bleve's internal order,
	// which varies between otherwise-identical searches. SortBy is applied before truncating to Size, so it also stabilizes which
	// tied hits survive the limit boundary.
	searchReq.SortBy([]string{"-_score", "_id"})
	return searchReq
}

// augmentedToolSearchQuery is buildToolSearchQuery, augmented with the
// underscore-segment enhancement: identifier queries often include only the meaningful
// segments of a longer tool name, so when the plain query's top `probeSize`
// hits contain no canonical exact match, an additional segment-aware clause
// is added to reward boundary matches over substring hits. Shared so the
// scoped searches make the SAME decision an unscoped SearchTools(queryStr,
// probeSize) call would — the decision is a function of the query text and
// the corpus alone, never of scope.
func (b *BleveIndex) augmentedToolSearchQuery(queryStr string, probeSize int) (*bquery.BooleanQuery, error) {
	boolQuery := buildToolSearchQuery(queryStr)

	// A bare token ("issue") matches an interior/trailing segment of a
	// keyword-analyzed tool_name ("create_issue") that no other clause can
	// see. Like the underscore enhancement below it is added only when no
	// exact tool-name match is already on top: any added clause shifts BM25
	// query normalization for every hit, and exact-name scores are frozen.
	segmentQuery := underscoreSegmentQuery(queryStr)
	bare := false
	if segmentQuery == nil {
		segmentQuery = bareSegmentQuery(queryStr)
		bare = true
	}
	if segmentQuery == nil {
		return boolQuery, nil
	}

	probe, err := b.index.Search(newToolSearchRequest(boolQuery, 0, probeSize))
	if err != nil {
		return nil, fmt.Errorf("underscore segment probe failed: %w", err)
	}
	for _, hit := range probe.Hits {
		if fieldsContainExactToolName(hit.Fields, queryStr) {
			return boolQuery, nil // exact match already at the top: no boost needed
		}
	}

	if bare {
		// Leave the query (and thus every score) untouched when no tool name
		// has the segment: the extra clause would still shift normalization.
		segProbe, err := b.index.Search(newToolSearchRequest(segmentQuery, 0, 1))
		if err != nil {
			return nil, fmt.Errorf("bare segment probe failed: %w", err)
		}
		if segProbe.Total == 0 {
			return boolQuery, nil
		}
	}

	boolQuery.AddShould(segmentQuery)
	return boolQuery, nil
}

// SearchTools searches for tools using multiple query strategies for better results
func (b *BleveIndex) SearchTools(queryStr string, limit int) ([]*config.SearchResult, error) {
	if queryStr == "" {
		return nil, fmt.Errorf("search query cannot be empty")
	}

	boolQuery, err := b.augmentedToolSearchQuery(queryStr, limit)
	if err != nil {
		return nil, err
	}

	b.logger.Debug("Searching tools with enhanced query", zap.String("query", queryStr), zap.Int("limit", limit))

	searchResult, err := b.index.Search(newToolSearchRequest(boolQuery, 0, limit))
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}

	// Convert results
	var results []*config.SearchResult
	for _, hit := range searchResult.Hits {
		results = append(results, &config.SearchResult{
			Tool:  readToolMetadata(hit.ID, hit.Fields),
			Score: hit.Score,
		})
	}

	b.logger.Debug("Found tools matching query", zap.Int("count", len(results)), zap.String("query", queryStr))
	return results, nil
}

// scopedSearchMinPage is the smallest page SearchToolsScoped walks the ranked
// result with; the page is max(limit, scopedSearchMinPage).
const scopedSearchMinPage = 256

// SearchToolsScoped is SearchTools for a caller who may see only some servers
// : the result is the top-
// `limit` of the SAME ranked search, filtered to servers `inScope` admits
// BEFORE the cut, with the unfiltered scores. It runs the identical boolean
// query — including the underscore-segment enhancement SearchTools(queryStr,
// limit) would apply (augmentedToolSearchQuery), and never a Must term on
// server_name (that would change scores) — and the identical score-then-id
// sort, and pages through the ranked result EXHAUSTIVELY with From/Size: each page is filtered through inScope, and
// paging stops only when `limit` in-scope hits have been collected or the
// window has passed searchResult.Total. There is deliberately NO result cap:
// any cap would let a hidden population larger than the cap displace an
// entitled hit, and make membership and `total` differ between a corpus that
// contains the hidden servers and one that does not — an existence oracle. A
// caller whose predicate is nil sees nothing (fail closed)
// without a search.
func (b *BleveIndex) SearchToolsScoped(queryStr string, limit int, inScope func(serverName string) bool) ([]*config.SearchResult, error) {
	if queryStr == "" {
		return nil, fmt.Errorf("search query cannot be empty")
	}
	if inScope == nil || limit <= 0 {
		return []*config.SearchResult{}, nil
	}

	q, err := b.augmentedToolSearchQuery(queryStr, limit)
	if err != nil {
		return nil, err
	}
	pageSize := limit
	if pageSize < scopedSearchMinPage {
		pageSize = scopedSearchMinPage
	}

	b.logger.Debug("Searching tools with scoped query", zap.String("query", queryStr), zap.Int("limit", limit))

	results := make([]*config.SearchResult, 0, limit)
	for from := 0; ; from += pageSize {
		searchResult, err := b.index.Search(newToolSearchRequest(q, from, pageSize))
		if err != nil {
			return nil, fmt.Errorf("search failed: %w", err)
		}
		for _, hit := range searchResult.Hits {
			tool := readToolMetadata(hit.ID, hit.Fields)
			if !inScope(tool.ServerName) {
				continue
			}
			results = append(results, &config.SearchResult{Tool: tool, Score: hit.Score})
			if len(results) >= limit {
				return results, nil
			}
		}
		if len(searchResult.Hits) == 0 || uint64(from+pageSize) >= searchResult.Total {
			break
		}
	}

	b.logger.Debug("Found scoped tools matching query", zap.Int("count", len(results)), zap.String("query", queryStr))
	return results, nil
}

// Hit is the canonical registration identity SearchToolsAdmitted hands to its
// predicate: the exact (server, raw tool) pair a hit resolves to. A caller
// that needs the tool's effective annotations resolves them itself, through
// the same identity resolution every dispatch path uses, keeping discovery
// and execution classifying from one source.
type Hit struct {
	Server string
	Tool   string
}

// Admission is admit's per-hit verdict for SearchToolsAdmitted.
type Admission int

const (
	// Admit means the hit is visible to this caller and counts toward limit.
	Admit Admission = iota
	// RejectScope means the hit's server is outside the caller's effective
	// scope (client token allowed_servers, profile server set). Never counted
	// in hiddenByPolicy — an out-of-scope tool must stay invisible, not merely
	// "hidden by profile".
	RejectScope
	// RejectPolicy means the hit's server was in scope but the tool policy
	// (profile.CompiledPolicy.Decide) excluded it. Counted in hiddenByPolicy.
	RejectPolicy
)

// SearchToolsAdmitted is SearchToolsScoped's hit-level counterpart: the
// pre-limit predicate sees the hit's canonical (server, tool) identity and
// returns one of three verdicts instead of a bool, so the caller
// can tell an out-of-scope rejection (never counted, stays invisible) from a
// policy-only one (counted in hiddenByPolicy, the `hidden_by_profile`
// figure) over the SAME exhaustive pre-limit scan SearchToolsScoped already
// runs. limit is applied to ADMITTED hits only — a rejected top hit, whatever
// its reason, never shortens the page — and hiddenByPolicy accumulates over
// the full match set, not merely the hits collected before the cut, so a
// caller whose page filled up before scanning every match still gets an
// accurate count.
//
// Identical to SearchToolsScoped in query construction (incl. the
// underscore-segment enhancement) and score-then-id sort. It differs in ONE
// respect, precisely because of the counting requirement above:
// SearchToolsScoped returns as soon as its page fills, while this method
// keeps paging to the end of the match set so every RejectPolicy hit ranked
// below the cut is still counted. The extra scanning costs work, never a
// different admitted page.
func (b *BleveIndex) SearchToolsAdmitted(queryStr string, limit int, admit func(Hit) Admission) (results []*config.SearchResult, hiddenByPolicy int, err error) {
	if queryStr == "" {
		return nil, 0, fmt.Errorf("search query cannot be empty")
	}
	if admit == nil || limit <= 0 {
		return []*config.SearchResult{}, 0, nil
	}

	q, err := b.augmentedToolSearchQuery(queryStr, limit)
	if err != nil {
		return nil, 0, err
	}
	pageSize := limit
	if pageSize < scopedSearchMinPage {
		pageSize = scopedSearchMinPage
	}

	b.logger.Debug("Searching tools with admitted query", zap.String("query", queryStr), zap.Int("limit", limit))

	// Not preallocated from limit: no caller-supplied value may size an
	// allocation, and result lists are small (callers clamp limit, e.g.
	// ProfilesService.Try caps it at 50), so append growth is negligible.
	results = []*config.SearchResult{}
	for from := 0; ; from += pageSize {
		searchResult, err := b.index.Search(newToolSearchRequest(q, from, pageSize))
		if err != nil {
			return nil, 0, fmt.Errorf("search failed: %w", err)
		}
		for _, hit := range searchResult.Hits {
			tool := readToolMetadata(hit.ID, hit.Fields)
			switch admit(Hit{Server: tool.ServerName, Tool: config.RawToolName(tool)}) {
			case Admit:
				// The page itself stops growing once it holds `limit`
				// admitted hits, but the scan continues: a RejectPolicy hit
				// ranked below the cut must still be counted.
				if len(results) < limit {
					results = append(results, &config.SearchResult{Tool: tool, Score: hit.Score})
				}
			case RejectPolicy:
				hiddenByPolicy++
			case RejectScope:
				// Invisible: never counted, never collected.
			}
		}
		if len(searchResult.Hits) == 0 || uint64(from+pageSize) >= searchResult.Total {
			break
		}
	}

	b.logger.Debug("Found admitted tools matching query", zap.Int("count", len(results)), zap.Int("hidden_by_policy", hiddenByPolicy), zap.String("query", queryStr))
	return results, hiddenByPolicy, nil
}

func fieldsContainExactToolName(fields map[string]interface{}, queryStr string) bool {
	for _, field := range []string{"tool_name", "full_tool_name"} {
		if value, ok := fields[field].(string); ok && value == queryStr {
			return true
		}
	}
	return false
}

// underscoreSegmentQuery matches each underscore-delimited query segment at a
// complete segment boundary in the keyword-indexed tool_name field. Segment
// order is intentionally irrelevant, but every segment is required.
func underscoreSegmentQuery(queryStr string) bquery.Query {
	segments := strings.Split(queryStr, "_")
	if len(segments) < 2 || len(segments) > maxUnderscoreSearchSegments {
		return nil
	}

	segmentQueries := make([]bquery.Query, 0, len(segments))
	for _, segment := range segments {
		if !isASCIIAlphanumeric(segment) {
			return nil
		}

		exact := bleve.NewTermQuery(segment)
		exact.SetField("tool_name")
		exact.SetBoost(underscoreSegmentBoost)

		prefix := bleve.NewPrefixQuery(segment + "_")
		prefix.SetField("tool_name")
		prefix.SetBoost(underscoreSegmentBoost)

		middle := bleve.NewWildcardQuery("*_" + segment + "_*")
		middle.SetField("tool_name")
		middle.SetBoost(underscoreSegmentBoost)

		suffix := bleve.NewWildcardQuery("*_" + segment)
		suffix.SetField("tool_name")
		suffix.SetBoost(underscoreSegmentBoost)

		segmentQueries = append(segmentQueries, bleve.NewDisjunctionQuery(exact, prefix, middle, suffix))
	}

	return bleve.NewConjunctionQuery(segmentQueries...)
}

// bareSegmentQuery matches a single alphanumeric token (>= bareSegmentMinLen
// chars, no underscore) as a non-leading segment of tool_name. The leading
// segment is already covered by the prefix clause. Both the query as typed and
// its lowercase form are tried. Returns nil for any other query shape.
func bareSegmentQuery(queryStr string) bquery.Query {
	if len(queryStr) < bareSegmentMinLen || !isASCIIAlphanumeric(queryStr) {
		return nil
	}
	forms := []string{queryStr}
	if lower := strings.ToLower(queryStr); lower != queryStr {
		forms = append(forms, lower)
	}
	queries := make([]bquery.Query, 0, len(forms)*2)
	for _, form := range forms {
		middle := bleve.NewWildcardQuery("*_" + form + "_*")
		middle.SetField("tool_name")
		suffix := bleve.NewWildcardQuery("*_" + form)
		suffix.SetField("tool_name")
		queries = append(queries, middle, suffix)
	}
	q := bleve.NewDisjunctionQuery(queries...)
	q.SetBoost(bareSegmentBoost)
	return q
}

func isASCIIAlphanumeric(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// DeleteAll removes every document from the index, leaving an empty index in
// place. Used to rebuild a per-profile index from scratch without recreating
// its on-disk directory.
func (b *BleveIndex) DeleteAll() error {
	_, err := b.deleteMatching(bleve.NewMatchAllQuery(), "failed to enumerate documents for delete-all", "failed to delete documents for delete-all")
	return err
}

func (b *BleveIndex) deleteMatching(query bquery.Query, searchFailure, deleteFailure string) (int, error) {
	deleted := 0
	for {
		searchReq := bleve.NewSearchRequest(query)
		searchReq.Size = searchPageSize
		searchResult, err := b.index.Search(searchReq)
		if err != nil {
			return deleted, fmt.Errorf("%s: %w", searchFailure, err)
		}
		if len(searchResult.Hits) == 0 {
			return deleted, nil
		}

		batch := b.index.NewBatch()
		for _, hit := range searchResult.Hits {
			batch.Delete(hit.ID)
		}
		if err := b.index.Batch(batch); err != nil {
			return deleted, fmt.Errorf("%s: %w", deleteFailure, err)
		}
		deleted += len(searchResult.Hits)
	}
}

// Batch operations for efficiency

// BatchIndex indexes multiple tools in a single batch
func (b *BleveIndex) BatchIndex(tools []*config.ToolMetadata) error {
	batch := b.index.NewBatch()

	for _, toolMeta := range tools {
		docID, doc := toolDocument(toolMeta)
		_ = batch.Index(docID, doc)
	}

	b.logger.Debug("Batch indexing tools", zap.Int("count", len(tools)))
	return b.index.Batch(batch)
}

// GetToolsByServer retrieves all tools from a specific server
func (b *BleveIndex) GetToolsByServer(serverName string) ([]*config.ToolMetadata, error) {
	// Create a term query for the server name
	query := bleve.NewTermQuery(serverName)
	query.SetField("server_name")

	fields := []string{"tool_name", "full_tool_name", "server_name", "description", "params_json", "output_schema_json", "hash", "annotations_json"}

	b.logger.Debug("Querying tools by server", zap.String("server", serverName))

	// Paginate so a server exposing more than one search page of tools is fully
	// covered — a single capped search would silently drop the overflow. A short final page (fewer hits than the page size) ends the loop.
	var tools []*config.ToolMetadata
	for from := 0; ; from += searchPageSize {
		searchReq := bleve.NewSearchRequest(query)
		searchReq.From = from
		searchReq.Size = searchPageSize
		searchReq.Fields = fields

		searchResult, err := b.index.Search(searchReq)
		if err != nil {
			return nil, fmt.Errorf("failed to query tools by server: %w", err)
		}

		for _, hit := range searchResult.Hits {
			tools = append(tools, readToolMetadata(hit.ID, hit.Fields))
		}

		if len(searchResult.Hits) < searchPageSize {
			break
		}
	}

	b.logger.Debug("Found tools for server",
		zap.String("server", serverName),
		zap.Int("count", len(tools)))

	return tools, nil
}

// GetAllIndexedServerNames returns the unique set of server names present in the index.
func (b *BleveIndex) GetAllIndexedServerNames() ([]string, error) {
	// Use a MatchAll query to scan every document, requesting only the server_name field
	query := bleve.NewMatchAllQuery()
	searchReq := bleve.NewSearchRequest(query)
	searchReq.Size = 0 // We only need facets, not results

	// Add a facet on server_name to get unique values
	facet := bleve.NewFacetRequest("server_name", 10000) // generous upper bound
	searchReq.AddFacet("servers", facet)

	searchResult, err := b.index.Search(searchReq)
	if err != nil {
		return nil, fmt.Errorf("failed to query indexed server names: %w", err)
	}

	facetResult, ok := searchResult.Facets["servers"]
	if !ok {
		return nil, nil // no facet result means no documents
	}

	var names []string
	for _, term := range facetResult.Terms.Terms() {
		names = append(names, term.Term)
	}

	b.logger.Debug("Retrieved indexed server names",
		zap.Int("count", len(names)))
	return names, nil
}

// Helper function to get string field from search results
func getStringField(fields map[string]interface{}, fieldName string) string {
	if val, ok := fields[fieldName]; ok {
		if strVal, ok := val.(string); ok {
			return strVal
		}
	}
	return ""
}
