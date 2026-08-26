package skillshare

import (
	"bufio"
	"fmt"
	"math"
	"net"
	"regexp"
	"strings"
)

// The CLI deliberately owns this preview scanner. The server remains the
// enforcement authority for uploaded skills; this local copy gives users fast,
// offline feedback without requiring a private Go module at install time.
const maxLineBytes = 4 * 1024 * 1024

type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarn     Severity = "warn"
)

type Finding struct {
	RuleID   string   `json:"ruleId"`
	Line     int      `json:"line"`
	Category string   `json:"category"`
	Severity Severity `json:"severity"`
	Excerpt  string   `json:"excerpt"`
}

type Verdict struct {
	Score         int         `json:"score"`
	Threshold     int         `json:"threshold"`
	Blocked       bool        `json:"blocked"`
	FiredRules    []FiredRule `json:"firedRules,omitempty"`
	PolicyVersion string      `json:"policyVersion"`
}

type FiredRule struct {
	RuleID      string    `json:"ruleId"`
	Name        string    `json:"name"`
	Severity    Severity  `json:"severity"`
	Occurrences int       `json:"occurrences"`
	Points      int       `json:"points"`
	Explanation string    `json:"explanation"`
	Findings    []Finding `json:"findings,omitempty"`
}

type Policy struct{ BlockThreshold int }

func DefaultPolicy() Policy { return Policy{BlockThreshold: 100} }

type detectorKind int

const (
	detectorRegex detectorKind = iota
	detectorAssignment
	detectorPrivateIP
)

type rule struct {
	ID, Name, Explanation string
	Severity              Severity
	Points                int
	re                    *regexp.Regexp
	kind                  detectorKind
}

var rules = []rule{
	{ID: "R-01", Name: "OpenAI API key", Severity: SeverityCritical, Points: 100, Explanation: "Remove the key and reference an environment variable instead.", re: regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`)},
	{ID: "R-02", Name: "Anthropic API key", Severity: SeverityCritical, Points: 100, Explanation: "Remove the key and reference an environment variable instead.", re: regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{16,}`)},
	{ID: "R-03", Name: "GitHub token", Severity: SeverityCritical, Points: 100, Explanation: "Revoke this token — it is now in shell history and any copy of the file.", re: regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{16,}`)},
	{ID: "R-04", Name: "GitLab token", Severity: SeverityCritical, Points: 100, Explanation: "Revoke this token and reference an environment variable instead.", re: regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{16,}`)},
	{ID: "R-05", Name: "Slack token", Severity: SeverityCritical, Points: 100, Explanation: "Revoke this token in the Slack admin console.", re: regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}`)},
	{ID: "R-06", Name: "AWS access key id", Severity: SeverityCritical, Points: 100, Explanation: "Rotate the key pair in IAM; the secret half is likely nearby.", re: regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{ID: "R-07", Name: "Google API key", Severity: SeverityCritical, Points: 100, Explanation: "Rotate the key in the Google Cloud console.", re: regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{ID: "R-08", Name: "Telara CLI token", Severity: SeverityCritical, Points: 100, Explanation: "Run 'telara logout' to revoke, then re-authenticate.", re: regexp.MustCompile(`\btlrc_[A-Za-z0-9_]{16,}`)},
	{ID: "R-09", Name: "Private key block", Severity: SeverityCritical, Points: 100, Explanation: "Remove the key material; treat the key as compromised.", re: regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{ID: "R-10", Name: "JSON Web Token", Severity: SeverityCritical, Points: 100, Explanation: "Remove the token; it may still be valid and carries claims.", re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)},
	{ID: "R-11", Name: "Credential assignment", Severity: SeverityCritical, Points: 100, Explanation: "Replace the literal value with an environment variable reference.", kind: detectorAssignment},
	{ID: "R-20", Name: "Internal hostname", Severity: SeverityWarn, Points: 10, Explanation: "Internal hostnames are reconnaissance value outside your network. Intentional in a team skill; reconsider before wider scopes.", re: regexp.MustCompile(`\b[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*\.(internal|local|corp|intranet|lan|private)\b`)},
	{ID: "R-21", Name: "Private IP address", Severity: SeverityWarn, Points: 10, Explanation: "Private addresses describe your network layout. Usually fine for a team skill.", kind: detectorPrivateIP},
}

var (
	assignmentRe  = regexp.MustCompile(`(?i)\b(api[_-]?key|secret|password|passwd|token|credential|auth)\b\s*[:=]\s*["']?([^\s"',]{8,})["']?`)
	placeholderRe = regexp.MustCompile(`(?i)^(<[^>]*>|\{\{.*\}\}|\$\{?[A-Z_]+\}?|x{6,}|\.{3,}|your[_-].*|example|changeme|redacted|placeholder|dummy|test|sample|todo|n/?a)$`)
	ruleByID      = func() map[string]rule {
		out := make(map[string]rule, len(rules))
		for _, r := range rules {
			out[r.ID] = r
		}
		return out
	}()
)

func Scan(body string) []Finding {
	var findings []Finding
	scanner := bufio.NewScanner(strings.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := scanner.Text()
		for _, r := range rules {
			switch r.kind {
			case detectorRegex:
				for _, match := range r.re.FindAllString(line, -1) {
					findings = append(findings, Finding{RuleID: r.ID, Line: lineNo, Category: r.Name, Severity: r.Severity, Excerpt: Truncate(match)})
				}
			case detectorAssignment:
				for _, match := range assignmentRe.FindAllStringSubmatch(line, -1) {
					value := strings.Trim(match[2], `"'`)
					if placeholderRe.MatchString(value) || !looksSecret(value) {
						continue
					}
					findings = append(findings, Finding{RuleID: r.ID, Line: lineNo, Category: r.Name + " (" + strings.ToLower(match[1]) + ")", Severity: r.Severity, Excerpt: Truncate(value)})
				}
			case detectorPrivateIP:
				for _, ip := range privateIPs(line) {
					findings = append(findings, Finding{RuleID: r.ID, Line: lineNo, Category: r.Name, Severity: r.Severity, Excerpt: ip})
				}
			}
		}
	}
	return dedupe(findings)
}

func HasCritical(findings []Finding) bool {
	for _, finding := range findings {
		if finding.Severity == SeverityCritical {
			return true
		}
	}
	return false
}

func ScanAndAssess(body string, policy Policy) ([]Finding, Verdict) {
	findings := Scan(body)
	return findings, Assess(findings, policy)
}

func Assess(findings []Finding, policy Policy) Verdict {
	if policy.BlockThreshold <= 0 {
		policy = DefaultPolicy()
	}
	byRule := map[string]*FiredRule{}
	order := []string{}
	for _, finding := range findings {
		r, ok := ruleByID[finding.RuleID]
		if !ok {
			r = rule{ID: finding.RuleID, Name: finding.Category, Severity: finding.Severity, Points: 100, Explanation: "Unrecognised rule id; treated as credential-grade."}
		}
		fired, seen := byRule[r.ID]
		if !seen {
			fired = &FiredRule{RuleID: r.ID, Name: r.Name, Severity: r.Severity, Explanation: r.Explanation}
			byRule[r.ID] = fired
			order = append(order, r.ID)
		}
		fired.Occurrences++
		fired.Points += r.Points
		fired.Findings = append(fired.Findings, finding)
	}
	verdict := Verdict{Threshold: policy.BlockThreshold, PolicyVersion: "skillscan/v1"}
	for _, id := range order {
		fired := byRule[id]
		verdict.Score += fired.Points
		verdict.FiredRules = append(verdict.FiredRules, *fired)
	}
	verdict.Blocked = verdict.Score >= verdict.Threshold
	return verdict
}

func Explain(verdict Verdict) []string {
	lines := make([]string, 0, len(verdict.FiredRules))
	for _, fired := range verdict.FiredRules {
		location := ""
		if len(fired.Findings) > 0 {
			location = fmt.Sprintf(" line %d", fired.Findings[0].Line)
			if len(fired.Findings) > 1 {
				location = fmt.Sprintf(" lines %d +%d more", fired.Findings[0].Line, len(fired.Findings)-1)
			}
		}
		lines = append(lines, fmt.Sprintf("%s %s%s (%s, %d pts) — %s", fired.RuleID, fired.Name, location, fired.Severity, fired.Points, fired.Explanation))
	}
	return lines
}

func Truncate(value string) string {
	const keep = 6
	if len(value) <= keep*2 {
		if len(value) <= keep {
			return value + "…"
		}
		return value[:keep] + "…"
	}
	return value[:keep] + "…" + value[len(value)-2:]
}

func looksSecret(value string) bool {
	return len(value) >= 12 && !strings.Contains(value, " ") && shannonEntropy(value) >= 3.2
}

func shannonEntropy(value string) float64 {
	counts := map[rune]float64{}
	for _, r := range value {
		counts[r]++
	}
	total := float64(len([]rune(value)))
	if total == 0 {
		return 0
	}
	var entropy float64
	for _, count := range counts {
		p := count / total
		entropy -= p * math.Log2(p)
	}
	return entropy
}

func privateIPs(line string) []string {
	var ips []string
	for _, field := range strings.FieldsFunc(line, func(r rune) bool { return !(r == '.' || r >= '0' && r <= '9') }) {
		ip := net.ParseIP(field)
		if ip != nil && ip.To4() != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()) {
			ips = append(ips, field)
		}
	}
	return ips
}

func dedupe(findings []Finding) []Finding {
	seen := make(map[string]bool, len(findings))
	out := findings[:0]
	for _, finding := range findings {
		key := fmt.Sprintf("%d|%s|%s", finding.Line, finding.RuleID, finding.Excerpt)
		if !seen[key] {
			seen[key] = true
			out = append(out, finding)
		}
	}
	return out
}
