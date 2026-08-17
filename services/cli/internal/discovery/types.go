package discovery

// CredentialClass describes the presence and handling style of credentials
// without retaining any credential value.
type CredentialClass string

const (
	CredentialInline        CredentialClass = "CredentialInline"
	CredentialEnvReferenced CredentialClass = "CredentialEnvReferenced"
	CredentialOAuthManaged  CredentialClass = "CredentialOAuthManaged"
	CredentialNone          CredentialClass = "CredentialNone"
	CredentialUnknown       CredentialClass = "CredentialUnknown"
)

// ScanStatus describes the outcome of reading one client/scope config file.
type ScanStatus string

const (
	ScanOK               ScanStatus = "ScanOK"
	ScanFileAbsent       ScanStatus = "ScanFileAbsent"
	ScanParseFailed      ScanStatus = "ScanParseFailed"
	ScanPermissionDenied ScanStatus = "ScanPermissionDenied"
	ScanUnsupported      ScanStatus = "ScanUnsupported"
)

const (
	TransportStdio   = "stdio"
	TransportHTTP    = "http"
	TransportSSE     = "sse"
	TransportUnknown = "unknown"
)

// DiscoveredServer describes one MCP server entry found in one config file.
type DiscoveredServer struct {
	ServerName      string          `json:"serverName"`
	Transport       string          `json:"transport"`
	EndpointHost    string          `json:"endpointHost,omitempty"`
	CommandIdentity string          `json:"commandIdentity,omitempty"`
	RawArgCount     int             `json:"rawArgCount"`
	CredentialClass CredentialClass `json:"credentialClass"`
	CredentialHint  string          `json:"credentialHint,omitempty"`
}

// ConfigScanResult describes scanning one client family and scope pair.
//
// Servers and Skills are separate asset classes sharing one scope result so that
// coverage and tombstone authority stay defined once (see report.go). A given
// scan populates one or the other, never both.
type ConfigScanResult struct {
	ClientFamily string             `json:"clientFamily"`
	Scope        string             `json:"scope"`
	PathClass    string             `json:"pathClass"`
	Servers      []DiscoveredServer `json:"servers"`
	Skills       []DiscoveredSkill  `json:"skills,omitempty"`
	Status       ScanStatus         `json:"status"`
	ErrorClass   string             `json:"errorClass,omitempty"`

	// DroppedRecords counts ai_estate resource records this scope's scan
	// produced whose identity could not be resolved (id_field, and every
	// id_fallback_fields path, all resolved empty). Per the engine-loop
	// contract (architecture/ai-estate-catalog-schema.md §7, property 3) an
	// unresolvable identity is DROPPED but must never be silently absent —
	// this is where the count surfaces so a caller can distinguish
	// "genuinely nothing here" from "something was unreadable."
	DroppedRecords int `json:"droppedRecords,omitempty"`

	// records carries every ai_estate resource this scan produced, tagged by
	// kind, before the mcp_server_deployment ones are folded into Servers
	// above. It is engine-internal (unexported, never marshalled) — see
	// catalog.go's package doc for why the wire shape emitted by report.go is
	// deliberately unchanged by the catalog-driven migration.
	records []engineRecord
}
