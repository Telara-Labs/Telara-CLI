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
}
