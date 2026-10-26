package runbook

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// SensitivePlaceholder marks, inside a sensitive pattern, the part to hide.
const SensitivePlaceholder = "{{SENSITIVE}}"

// BuiltinSensitive patterns apply to every runbook: credential-shaped text
// is masked even if it was never declared secret.
var BuiltinSensitive = map[string]string{
	"url password": `[A-Za-z][A-Za-z0-9+.-]*://[^\s:/@]+:{{SENSITIVE}}@`,
	"bearer token": `(?i)\bbearer\s+{{SENSITIVE}}`,
	"aws secret":   `(?i)\b(aws_secret_access_key|aws_session_token)["']?\s*[=:]\s*["']?{{SENSITIVE}}`,
	// password=…, PGPASSWORD=…, "api_key": "…", --token=…, client_secret: …
	"credential setting": `(?i)[a-z0-9_.-]*(` + credentialWords + `)["']?\s*[=:]\s*["']?{{SENSITIVE}}`,
	// --password …, --token …
	"credential flag": `(?i)--[a-z0-9-]*(` + credentialWords + `)\s+["']?{{SENSITIVE}}`,
}

// credentialWords are the names credentials go by.
const credentialWords = `passw(?:or)?d|passphrase|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|mysql_pwd|credentials?`

var credentialName = regexp.MustCompile(`(?i)(` + credentialWords + `|pass$|^pw$|_pw$)`)

// SensitiveName says whether a variable's name says it holds a credential
// (db_password, api_token, dbpass): published, it's kept secret.
func SensitiveName(name string) bool { return credentialName.MatchString(name) }

const groupPrefix = "runnrsensitive"

// CompileSensitive turns a pattern into a regexp whose groups named
// runnrsensitiveN are the parts to hide. The rest of the pattern is a
// regular expression; {{SENSITIVE}} matches a run of non-space,
// non-quote characters (as few as the pattern allows).
func CompileSensitive(pattern string) (*regexp.Regexp, error) {
	if !strings.Contains(pattern, SensitivePlaceholder) {
		return nil, errors.New("must contain " + SensitivePlaceholder)
	}
	parts := strings.Split(pattern, SensitivePlaceholder)
	var b strings.Builder
	for i, p := range parts {
		b.WriteString(p)
		if i == len(parts)-1 {
			break
		}
		tok := "[^\\s\"'`]+?"
		if i == len(parts)-2 && parts[i+1] == "" {
			tok = "[^\\s\"'`]+" // at the end, take the whole token
		}
		fmt.Fprintf(&b, "(?P<%s%d>%s)", groupPrefix, i, tok)
	}
	return regexp.Compile(b.String())
}

// Masker hides credentials in text using sensitive patterns.
type Masker struct{ res []*regexp.Regexp }

// NewMasker uses the built-in patterns plus the runbook's (which must
// have been validated).
func NewMasker(rb *Runbook) *Masker {
	m := &Masker{}
	var names []string
	all := map[string]string{}
	for k, v := range BuiltinSensitive {
		all["builtin:"+k] = v
	}
	if rb != nil {
		for k, v := range rb.Sensitive {
			all[k] = v
		}
	}
	for k := range all {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if re, err := CompileSensitive(all[k]); err == nil {
			m.res = append(m.res, re)
		}
	}
	return m
}

// Mask replaces the sensitive parts of every match with ****.
func (m *Masker) Mask(s string) string {
	for _, re := range m.res {
		matches := re.FindAllStringSubmatchIndex(s, -1)
		if matches == nil {
			continue
		}
		names := re.SubexpNames()
		var spans [][2]int
		for _, mt := range matches {
			for g := 1; g < len(names); g++ {
				if strings.HasPrefix(names[g], groupPrefix) && mt[2*g] >= 0 {
					spans = append(spans, [2]int{mt[2*g], mt[2*g+1]})
				}
			}
		}
		sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
		var b strings.Builder
		last := 0
		for _, sp := range spans {
			if sp[0] < last {
				continue
			}
			b.WriteString(s[last:sp[0]])
			b.WriteString("****")
			last = sp[1]
		}
		b.WriteString(s[last:])
		s = b.String()
	}
	return s
}
