package plex

import "encoding/json"

// Connection is one way to reach a Plex server, as returned by plex.tv
// /api/v2/resources. Quality and Label are computed by RankConnections on
// the winning connection only and are not part of the wire/JSON shape
// (JS attaches them to the ranked winner object for the UI badge).
type Connection struct {
	URI     string `json:"uri"`
	Local   bool   `json:"local"`
	Relay   bool   `json:"relay"`
	Quality string `json:"-"`
	Label   string `json:"-"`
}

// Server is one discovered Plex Media Server. Token here is the per-server
// accessToken — the ONLY credential used for server calls. The plex.tv user
// token is not sent to the media server.
type Server struct {
	Name           string       `json:"name"`
	Machine        string       `json:"machine"`
	Owned          bool         `json:"owned"`
	Token          string       `json:"token"`
	Connections    []Connection `json:"connections"`
	BestConnection Connection   `json:"bestConnection"`
}

// Section is one library section (Movies / Shows / ...). Only numeric-keyed
// movie+show sections are returned by GetSections — the app doesn't manage
// music/photos in v0.1.
type Section struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Type  string `json:"type"`
	UUID  string `json:"uuid"`
}

// Collection is one collection in a section, in exactly the shape of the JS
// getCollections return (camelCase keys are the frontend contract).
// promotedTo* are visibility flags Plex stores via hubs; read here so the
// Builder can show them, written back by UpdateCollectionMeta.
type Collection struct {
	RatingKey  string `json:"ratingKey"`
	Title      string `json:"title"`
	ChildCount int    `json:"childCount"`
	Smart      bool   `json:"smart"`
	// Content is the smart collection's stored filter URI, verbatim as
	// the server reports it (v1.14: PMS 1.43 exposes it on both the
	// section listing and the collection record — verified live on the
	// sandbox PMS 1.43.5, 2026-10-23). Empty for manual collections.
	Content string `json:"content,omitempty"`
	// SmartSummary is Content translated to plain words for display
	// (SmartRuleSummary). Empty unless Smart and the rule parses.
	SmartSummary          string `json:"smartSummary,omitempty"`
	Thumb                 string `json:"thumb"`
	Art                   string `json:"art"`
	Summary               string `json:"summary"`
	TitleSort             string `json:"titleSort"`
	AddedAt               int64  `json:"addedAt"`
	UpdatedAt             int64  `json:"updatedAt"`
	PromotedToOwnHome     bool   `json:"promotedToOwnHome"`
	PromotedToSharedHome  bool   `json:"promotedToSharedHome"`
	PromotedToRecommended bool   `json:"promotedToRecommended"`
	// v1.13 (collections manager): health facts derived from the list
	// record itself. HasPoster is true only when the record's thumb is
	// a real metadata thumb — a "/library/collections/{id}/composite/"
	// thumb is Plex's auto-generated mosaic, i.e. NO custom poster.
	// HasBackdrop mirrors it for the art path (absent = none set).
	// VisibilityKnown is false when the server did not answer the hub
	// manage read at all (PMS 1.43+ retired that endpoint): the three
	// Promoted* flags then mean "not reported", never "library only".
	HasPoster       bool `json:"hasPoster"`
	HasBackdrop     bool `json:"hasBackdrop"`
	VisibilityKnown bool `json:"visibilityKnown"`
}

// CollectionDetail is the manager editor's read model for ONE
// collection (GET /library/metadata/{id}): every field Plex exposes
// for a collection, nothing invented. Labels is a POINTER on purpose:
// nil means the server did not return a Label array for this
// collection at all (PMS 1.43 omits it), which is a different fact
// from "the collection has no labels" (non-nil, empty) — the editor
// keeps them distinct and never fabricates the missing field.
type CollectionDetail struct {
	RatingKey     string    `json:"ratingKey"`
	Title         string    `json:"title"`
	TitleSort     string    `json:"titleSort"`
	Summary       string    `json:"summary"`
	ContentRating string    `json:"contentRating"`
	Labels        *[]string `json:"labels"`
	ChildCount    int       `json:"childCount"`
	Smart         bool      `json:"smart"`
	// Content / SmartSummary: same contract as Collection (v1.14).
	Content          string `json:"content,omitempty"`
	SmartSummary     string `json:"smartSummary,omitempty"`
	Thumb            string `json:"thumb"`
	Art              string `json:"art"`
	LibrarySectionID string `json:"librarySectionID"`
	// Visibility is "home" | "shared" | "library" | "unknown".
	// "unknown" pairs with VisibilityKnown=false and a
	// VisibilityNote explaining why (the server does not report
	// promotion state) — the editor renders it read-only.
	Visibility      string `json:"visibility"`
	VisibilityKnown bool   `json:"visibilityKnown"`
	VisibilityNote  string `json:"visibilityNote,omitempty"`
}

// Item is EXACTLY the JS normalizeItem() output shape. Year is a pointer so
// a missing/zero Plex year marshals as JSON null, like the JS.
// Tagline, TitleSort and ContentRating are only populated by GetItem (the
// JS getItem spreads normalizeItem and adds those three keys); they carry
// omitempty so plain normalizeItem output keeps its exact shape.
// Index, LeafCount, ViewedLeafCount and ParentTitle are only populated by
// GetChildrenItems (seasons of a show, for the Overlay Studio drill-down);
// same omitempty reasoning — every other producer's shape is unchanged.
type Item struct {
	RatingKey     string `json:"ratingKey"`
	Title         string `json:"title"`
	Type          string `json:"type"`
	Year          *int   `json:"year"`
	Summary       string `json:"summary"`
	Thumb         string `json:"thumb"`
	Duration      int64  `json:"duration"`
	ViewOffset    int64  `json:"viewOffset"`
	AddedAt       int64  `json:"addedAt"`
	Key           string `json:"key"`
	Tagline       string `json:"tagline,omitempty"`
	TitleSort     string `json:"titleSort,omitempty"`
	ContentRating string `json:"contentRating,omitempty"`

	Index           int    `json:"index,omitempty"`           // season/episode number (children only)
	LeafCount       int    `json:"leafCount,omitempty"`       // episodes under a season (children only)
	ViewedLeafCount int    `json:"viewedLeafCount,omitempty"` // watched of those (children only)
	ParentTitle     string `json:"parentTitle,omitempty"`     // the parent item's title (children only)
	ParentRatingKey string `json:"parentRatingKey,omitempty"` // the parent item's ratingKey (children only)
}

// Tags is the v0.4 Plex-native Tag[] pull for the Advisor, in the exact
// shape of the JS getSectionItemsAll tags object.
type Tags struct {
	Genre      []string `json:"genre"`
	Director   []string `json:"director"`
	Writer     []string `json:"writer"`
	Actor      []string `json:"actor"`
	Studio     []string `json:"studio"`
	Country    []string `json:"country"`
	Collection []string `json:"collection"`
}

// LibraryItem is one entry of GetSectionItemsAll, in exactly the JS shape:
// raw-ish entries WITH Guids (includeGuids=1) so the Advisor can join to
// TMDb without fuzzy title matching.
// FUTURE AI: keep the raw Guids — normalizeItem drops them, and the TMDb
// join in the advisor needs them.
type LibraryItem struct {
	RatingKey     string   `json:"ratingKey"`
	Title         string   `json:"title"`
	Year          *int     `json:"year"`
	Type          string   `json:"type"`
	Guids         []string `json:"guids"`
	ViewCount     int      `json:"viewCount"`
	ViewOffset    int      `json:"viewOffset"`
	ChildCount    int      `json:"childCount"`
	Thumb         string   `json:"thumb"`
	AddedAt       int64    `json:"addedAt"`
	PlexRating    *float64 `json:"plexRating"`
	DurationMin   *int     `json:"durationMin"`
	SectionKey    string   `json:"sectionKey"`
	Tags          Tags     `json:"tags"`
	ContentRating string   `json:"contentRating"`
}

// PinResult is the JS pinStart return minus the PIN code itself: the code
// is embedded in AuthURL and the renderer only ever sees { pinId, authUrl }.
// PinID is numeric here (plex.tv issues numeric ids; the JS stringified it).
type PinResult struct {
	PinID     int64  `json:"pinId"`
	AuthURL   string `json:"authUrl"`
	ExpiresIn int    `json:"expiresIn"`
}

// CreatePayload is the JS createCollection argument object. Vis* are
// pointers because the JS distinguishes an absent flag (undefined — leave
// Plex's value alone) from an explicit true/false.
type CreatePayload struct {
	SectionKey     string   `json:"sectionKey"`
	Type           int      `json:"type"`
	Title          string   `json:"title"`
	Smart          bool     `json:"smart"`
	ItemRatingKeys []string `json:"itemRatingKeys"`
	FilterQuery    string   `json:"filterQuery"`
	TitleSort      string   `json:"titleSort"`
	Summary        string   `json:"summary"`
	ContentRating  string   `json:"contentRating"` // v1.17: Builder wizard Details step; applied via the same verified MetaFields write in CreateCollection (only when non-empty)
	VisHome        *bool    `json:"visHome"`
	VisShared      *bool    `json:"visShared"`
	VisRecommended *bool    `json:"visRecommended"`
}

// MetaFields is the JS updateCollectionMeta argument object. Every field is
// a pointer: nil means "undefined" in JS — the parameter is not sent at all.
// A non-nil pointer (even to "") IS sent.
type MetaFields struct {
	Title          *string   `json:"title"`
	TitleSort      *string   `json:"titleSort"`
	Summary        *string   `json:"summary"`
	ContentRating  *string   `json:"contentRating"` // v1.13: manager editor
	Labels         *[]string `json:"labels"`        // v1.13: manager editor (nil = don't send)
	VisHome        *bool     `json:"visHome"`
	VisShared      *bool     `json:"visShared"`
	VisRecommended *bool     `json:"visRecommended"`
}

// userShape is the mapped JS getUser return: { id, name, thumb }.
type userShape struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Thumb string `json:"thumb"`
}

// tagCountShape is one mapped JS listSectionTags entry: { name, count }.
type tagCountShape struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// createdShape is the JS createCollection success return: { ratingKey, title }
// (plus an honest note when Plex returns no id).
type createdShape struct {
	RatingKey string `json:"ratingKey"`
	Title     string `json:"title"`
	Note      string `json:"note,omitempty"`
}

// rawMessage marshals v to json.RawMessage; used where the JS returns a
// small mapped object and the Go contract carries it as raw JSON.
func rawMessage(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}
